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

func TestSQLStatementSplitterPreservesQuotedAndCommentSemicolons(t *testing.T) {
	t.Parallel()

	source := `INSERT INTO fixture_item (name) VALUES ('single;quoted', "double;quoted");
-- keep this; comment with the next statement
INSERT INTO fixture_item (` + "`name;column`" + `) VALUES ('it''s;quoted');
# keep this; hash comment with the next statement
UPDATE fixture_item SET name = 'backslash\';still quoted';
/* keep this; block comment with the next statement */
DELETE FROM fixture_item WHERE name = 'done';`

	got, err := splitSQLStatements(source)
	if err != nil {
		t.Fatalf("split SQL statements: %v", err)
	}
	want := []string{
		`INSERT INTO fixture_item (name) VALUES ('single;quoted', "double;quoted")`,
		"-- keep this; comment with the next statement\n" +
			"INSERT INTO fixture_item (`name;column`) VALUES ('it''s;quoted')",
		"# keep this; hash comment with the next statement\n" +
			`UPDATE fixture_item SET name = 'backslash\';still quoted'`,
		"/* keep this; block comment with the next statement */\n" +
			`DELETE FROM fixture_item WHERE name = 'done'`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("split statements = %#v, want %#v", got, want)
	}
}

func TestSQLStatementSplitterRejectsUnterminatedTokens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		source string
		want   string
	}{
		{name: "single quote", source: "SELECT 'open", want: "single-quoted string"},
		{name: "double quote", source: `SELECT "open`, want: "double-quoted string"},
		{name: "backtick", source: "SELECT `open", want: "backtick-quoted identifier"},
		{name: "block comment", source: "SELECT 1 /* open", want: "block comment"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := splitSQLStatements(test.source)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("split error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestSQLStatementSplitterSkipsCommentOnlyFragments(t *testing.T) {
	t.Parallel()

	source := `-- leading; comment
CREATE TABLE fixture_item (id INT);
# trailing; comment
/* trailing; block comment */`
	got, err := splitSQLStatements(source)
	if err != nil {
		t.Fatalf("split SQL statements: %v", err)
	}
	want := []string{
		"-- leading; comment\nCREATE TABLE fixture_item (id INT)",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("split statements = %#v, want %#v", got, want)
	}

	commentsOnly := "# hash; comment\n-- dash; comment\n/* block; comment */"
	got, err = splitSQLStatements(commentsOnly)
	if err != nil {
		t.Fatalf("split comment-only SQL: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("comment-only statements = %#v, want none", got)
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
