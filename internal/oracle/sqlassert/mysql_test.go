package sqlassert

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"

	"github.com/weavegate/weavegate/internal/fixture"
	"github.com/weavegate/weavegate/internal/oracle"
)

func TestMySQLReadOnlyAssertion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	runner := fixture.NewMySQLFixture()
	t.Cleanup(func() {
		if err := runner.Teardown(context.Background()); err != nil {
			t.Errorf("cleanup SQL assertion MySQL fixture: %v", err)
		}
	})
	db, err := runner.Provision(ctx, fixture.FixtureSpec{
		Image:      "mysql:8.4",
		Migrations: filepath.Join("..", "..", "fixture", "testdata", "mysql", "migration"),
		Seed:       filepath.Join("..", "..", "fixture", "testdata", "mysql", "seed.sql"),
	})
	if err != nil {
		t.Fatalf("provision SQL assertion MySQL fixture: %v", err)
	}

	before := readFixtureItemState(t, ctx, db)
	if want := (fixtureItemState{count: 1, name: "seed;value"}); before != want {
		t.Fatalf("fixture state before mutating assertion = %+v, want seed %+v", before, want)
	}
	assertion, err := NewZeroRow(
		"mutation-must-fail",
		"UPDATE fixture_item SET name = 'mutated' WHERE id = 1",
	)
	if err != nil {
		t.Fatalf("create mutating SQL assertion: %v", err)
	}
	violations, err := assertion.Evaluate(ctx, db.SQL, oracle.RunContext{})
	if err == nil {
		t.Fatal("mutating SQL assertion error = nil, want read-only rejection")
	}
	if violations != nil {
		t.Fatalf("mutating SQL assertion violations = %#v, want nil on evaluation error", violations)
	}
	var mysqlErr *mysqldriver.MySQLError
	if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1792 {
		t.Fatalf("mutating SQL assertion error = %v, want MySQL read-only error 1792", err)
	}

	after := readFixtureItemState(t, ctx, db)
	if after != before {
		t.Fatalf("fixture state after mutating assertion = %+v, want unchanged %+v", after, before)
	}

	t.Log("SQL_ASSERT_MYSQL_READONLY_RESULT write=error state_unchanged=true")
}

type fixtureItemState struct {
	count int
	name  string
}

func readFixtureItemState(
	t *testing.T,
	ctx context.Context,
	db *fixture.DB,
) fixtureItemState {
	t.Helper()
	var state fixtureItemState
	if err := db.SQL.QueryRowContext(
		ctx,
		"SELECT COUNT(*), COALESCE(MIN(name), '') FROM fixture_item",
	).Scan(&state.count, &state.name); err != nil {
		t.Fatalf("read fixture item state: %v", err)
	}
	return state
}
