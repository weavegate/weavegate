package fixture

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestSQLLoader(t *testing.T) {
	t.Parallel()

	executor := &recordingExecutor{}
	spec := FixtureSpec{
		Migrations: "testdata/loader/migration",
		Seed:       "testdata/loader/seed.sql",
	}

	if err := applyFixtureSQL(context.Background(), executor, spec); err != nil {
		t.Fatalf("apply fixture SQL: %v", err)
	}

	want := []string{
		"INSERT INTO execution_log (step) VALUES ('migration-001')",
		"INSERT INTO execution_log (step) VALUES ('migration-002')",
		"INSERT INTO execution_log (step) VALUES ('seed')",
	}
	if !reflect.DeepEqual(executor.statements, want) {
		t.Fatalf("execution order = %#v, want %#v", executor.statements, want)
	}

	t.Log("FIXTURE_LOADER_RESULT migrations=2 seed=1 order=lexicographic")
}

func TestSQLLoaderErrorContext(t *testing.T) {
	t.Parallel()

	executor := &recordingExecutor{failAt: 2, err: errors.New("execute failed")}
	spec := FixtureSpec{
		Migrations: "testdata/loader/migration",
		Seed:       "testdata/loader/seed.sql",
	}

	err := applyFixtureSQL(context.Background(), executor, spec)
	if err == nil {
		t.Fatal("apply fixture SQL returned nil, want error")
	}

	for _, part := range []string{"migration", "002_second.sql", "statement 1", "execute failed"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error %q does not contain %q", err, part)
		}
	}
}

type recordingExecutor struct {
	statements []string
	failAt     int
	err        error
}

func (e *recordingExecutor) ExecContext(
	_ context.Context,
	statement string,
	_ ...any,
) (sql.Result, error) {
	e.statements = append(e.statements, statement)
	if e.failAt > 0 && len(e.statements) == e.failAt {
		return nil, e.err
	}

	return staticResult{}, nil
}

type staticResult struct{}

func (staticResult) LastInsertId() (int64, error) {
	return 0, nil
}

func (staticResult) RowsAffected() (int64, error) {
	return 0, nil
}
