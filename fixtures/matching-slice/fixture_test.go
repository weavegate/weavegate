package matchingslice

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/weavegate/weavegate/internal/fixture"
)

const matchingRequestID int64 = 42

var matchingFixtureSpec = fixture.FixtureSpec{
	Image:      "mysql:8.4",
	Migrations: "db/migration",
	Seed:       "db/seed.sql",
}

func TestMatchingFixtureReset(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	runner := fixture.NewMySQLFixture()
	t.Cleanup(func() {
		if err := runner.Teardown(context.Background()); err != nil {
			t.Errorf("cleanup matching fixture: %v", err)
		}
	})

	prepared, err := fixture.Prepare(matchingFixtureSpec)
	if err != nil {
		t.Fatalf("prepare matching fixture: %v", err)
	}
	db, err := runner.Provision(ctx, prepared)
	if err != nil {
		t.Fatalf("provision matching fixture: %v", err)
	}

	assertMatchingSchema(t, ctx, db.SQL)
	assertMatchingSeed(t, ctx, db.SQL)

	sessionResult, err := db.SQL.ExecContext(
		ctx,
		"INSERT INTO matching_session (status) VALUES (?)",
		"ACTIVE",
	)
	if err != nil {
		t.Fatalf("insert matching session: %v", err)
	}
	sessionID, err := sessionResult.LastInsertId()
	if err != nil {
		t.Fatalf("read matching session ID: %v", err)
	}

	if _, err := db.SQL.ExecContext(
		ctx,
		`INSERT INTO assignment
            (project_request_id, matching_session_id, status)
            VALUES (?, ?, ?)`,
		matchingRequestID,
		sessionID,
		"ACTIVE",
	); err != nil {
		t.Fatalf("insert assignment: %v", err)
	}

	assertTableCounts(t, ctx, db.SQL, tableCounts{
		projectRequests:  1,
		matchingSessions: 1,
		assignments:      1,
	})

	if err := runner.Reset(ctx); err != nil {
		t.Fatalf("reset matching fixture: %v", err)
	}

	assertMatchingSeed(t, ctx, db.SQL)
	t.Log(
		"MATCHING_FIXTURE_RESULT request_id=42 project_requests=1 " +
			"matching_sessions=0 assignments=0 reset=true",
	)
}

func assertMatchingSchema(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()

	rows, err := db.QueryContext(
		ctx,
		`SELECT table_name, engine
         FROM information_schema.tables
         WHERE table_schema = DATABASE()
           AND table_name IN ('project_request', 'matching_session', 'assignment')`,
	)
	if err != nil {
		t.Fatalf("query matching tables: %v", err)
	}
	defer rows.Close()

	engines := make(map[string]string)
	for rows.Next() {
		var table string
		var engine string
		if err := rows.Scan(&table, &engine); err != nil {
			t.Fatalf("scan matching table: %v", err)
		}
		engines[table] = engine
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate matching tables: %v", err)
	}

	for _, table := range []string{"project_request", "matching_session", "assignment"} {
		engine, ok := engines[table]
		if !ok {
			t.Errorf("matching table %q is missing", table)
			continue
		}
		if engine != "InnoDB" {
			t.Errorf("matching table %q engine = %q, want InnoDB", table, engine)
		}
	}

	assertAssignmentForeignKeys(t, ctx, db)
	assertProjectRequestIsNotUnique(t, ctx, db)
}

func assertAssignmentForeignKeys(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()

	rows, err := db.QueryContext(
		ctx,
		`SELECT column_name, referenced_table_name
         FROM information_schema.key_column_usage
         WHERE table_schema = DATABASE()
           AND table_name = 'assignment'
           AND referenced_table_name IS NOT NULL`,
	)
	if err != nil {
		t.Fatalf("query assignment foreign keys: %v", err)
	}
	defer rows.Close()

	references := make(map[string]string)
	for rows.Next() {
		var column string
		var table string
		if err := rows.Scan(&column, &table); err != nil {
			t.Fatalf("scan assignment foreign key: %v", err)
		}
		references[column] = table
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate assignment foreign keys: %v", err)
	}

	want := map[string]string{
		"project_request_id":  "project_request",
		"matching_session_id": "matching_session",
	}
	for column, table := range want {
		if got := references[column]; got != table {
			t.Errorf("assignment foreign key %q references %q, want %q", column, got, table)
		}
	}
}

func assertProjectRequestIsNotUnique(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()

	var uniqueIndexes int
	if err := db.QueryRowContext(
		ctx,
		`SELECT COUNT(*)
         FROM information_schema.statistics
         WHERE table_schema = DATABASE()
           AND table_name = 'assignment'
           AND column_name = 'project_request_id'
           AND non_unique = 0`,
	).Scan(&uniqueIndexes); err != nil {
		t.Fatalf("query assignment unique indexes: %v", err)
	}
	if uniqueIndexes != 0 {
		t.Errorf("assignment project_request_id unique index count = %d, want 0", uniqueIndexes)
	}
}

func assertMatchingSeed(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()

	assertTableCounts(t, ctx, db, tableCounts{
		projectRequests:  1,
		matchingSessions: 0,
		assignments:      0,
	})

	var status string
	if err := db.QueryRowContext(
		ctx,
		"SELECT status FROM project_request WHERE id = ?",
		matchingRequestID,
	).Scan(&status); err != nil {
		t.Fatalf("read seeded project request: %v", err)
	}
	if status != "ACTIVE" {
		t.Errorf("seeded project request status = %q, want ACTIVE", status)
	}
}

type tableCounts struct {
	projectRequests  int
	matchingSessions int
	assignments      int
}

func assertTableCounts(t *testing.T, ctx context.Context, db *sql.DB, want tableCounts) {
	t.Helper()

	got := tableCounts{
		projectRequests:  queryCount(t, ctx, db, "project_request"),
		matchingSessions: queryCount(t, ctx, db, "matching_session"),
		assignments:      queryCount(t, ctx, db, "assignment"),
	}
	if got != want {
		t.Errorf("matching table counts = %+v, want %+v", got, want)
	}
}

func queryCount(t *testing.T, ctx context.Context, db *sql.DB, table string) int {
	t.Helper()

	var count int
	query := fmt.Sprintf("SELECT COUNT(*) FROM `%s`", table)
	if err := db.QueryRowContext(ctx, query).Scan(&count); err != nil {
		t.Fatalf("count %s rows: %v", table, err)
	}

	return count
}
