package fixture

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type statementExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type sqlSource struct {
	phase string
	path  string
}

type sqlScanState uint8

const (
	sqlStateNormal sqlScanState = iota
	sqlStateSingleQuoted
	sqlStateDoubleQuoted
	sqlStateBacktickQuoted
	sqlStateLineComment
	sqlStateBlockComment
)

// applyFixtureSQL applies migrations in lexical filename order, followed by
// the fixture's seed file.
func applyFixtureSQL(
	ctx context.Context,
	executor statementExecutor,
	spec FixtureSpec,
) error {
	if executor == nil {
		return fmt.Errorf("apply fixture SQL: executor is required")
	}

	migrations, err := migrationSources(spec.Migrations)
	if err != nil {
		return err
	}

	seed, err := seedSource(spec.Seed)
	if err != nil {
		return err
	}

	sources := append(migrations, seed)
	for _, source := range sources {
		if err := executeSQLSource(ctx, executor, source); err != nil {
			return err
		}
	}

	return nil
}

func migrationSources(directory string) ([]sqlSource, error) {
	if strings.TrimSpace(directory) == "" {
		return nil, fmt.Errorf("load migrations: directory is required")
	}

	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read migration directory %q: %w", directory, err)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	var sources []sqlSource
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".sql" {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("inspect migration file %q: %w", entry.Name(), err)
		}
		if !info.Mode().IsRegular() {
			continue
		}

		sources = append(sources, sqlSource{
			phase: "migration",
			path:  filepath.Join(directory, entry.Name()),
		})
	}

	return sources, nil
}

func seedSource(path string) (sqlSource, error) {
	if strings.TrimSpace(path) == "" {
		return sqlSource{}, fmt.Errorf("load seed: file is required")
	}

	info, err := os.Stat(path)
	if err != nil {
		return sqlSource{}, fmt.Errorf("inspect seed file %q: %w", filepath.Base(path), err)
	}
	if !info.Mode().IsRegular() {
		return sqlSource{}, fmt.Errorf("inspect seed file %q: not a regular file", filepath.Base(path))
	}

	return sqlSource{phase: "seed", path: path}, nil
}

func executeSQLSource(
	ctx context.Context,
	executor statementExecutor,
	source sqlSource,
) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("execute %s file %q: %w", source.phase, filepath.Base(source.path), err)
	}

	contents, err := os.ReadFile(source.path)
	if err != nil {
		return fmt.Errorf("read %s file %q: %w", source.phase, filepath.Base(source.path), err)
	}

	statements, err := splitSQLStatements(string(contents))
	if err != nil {
		return fmt.Errorf("parse %s file %q: %w", source.phase, filepath.Base(source.path), err)
	}

	for statementIndex, statement := range statements {
		if _, err := executor.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf(
				"execute %s file %q statement %d: %w",
				source.phase,
				filepath.Base(source.path),
				statementIndex+1,
				err,
			)
		}
	}

	return nil
}

// splitSQLStatements handles ordinary MySQL DDL and DML. The fixture contract
// intentionally excludes DELIMITER directives and stored-program bodies.
func splitSQLStatements(source string) ([]string, error) {
	var statements []string
	var statement strings.Builder
	state := sqlStateNormal
	hasExecutableToken := false

	appendStatement := func() {
		trimmed := strings.TrimSpace(statement.String())
		if hasExecutableToken && trimmed != "" {
			statements = append(statements, trimmed)
		}
		statement.Reset()
		hasExecutableToken = false
	}

	for i := 0; i < len(source); i++ {
		current := source[i]
		if state == sqlStateNormal && current == ';' {
			appendStatement()
			continue
		}
		statement.WriteByte(current)

		switch state {
		case sqlStateNormal:
			switch current {
			case '\'':
				hasExecutableToken = true
				state = sqlStateSingleQuoted
			case '"':
				hasExecutableToken = true
				state = sqlStateDoubleQuoted
			case '`':
				hasExecutableToken = true
				state = sqlStateBacktickQuoted
			case '#':
				state = sqlStateLineComment
			case '-':
				if startsDashComment(source, i) {
					statement.WriteByte(source[i+1])
					i++
					state = sqlStateLineComment
				} else {
					hasExecutableToken = true
				}
			case '/':
				if i+1 < len(source) && source[i+1] == '*' {
					statement.WriteByte(source[i+1])
					i++
					if i+1 < len(source) && source[i+1] == '!' {
						hasExecutableToken = true
					}
					state = sqlStateBlockComment
				} else {
					hasExecutableToken = true
				}
			default:
				if current > ' ' {
					hasExecutableToken = true
				}
			}

		case sqlStateSingleQuoted:
			i = consumeStringByte(source, i, '\'', &statement, &state)
		case sqlStateDoubleQuoted:
			i = consumeStringByte(source, i, '"', &statement, &state)
		case sqlStateBacktickQuoted:
			i = consumeBacktickIdentifierByte(source, i, &statement, &state)
		case sqlStateLineComment:
			if current == '\n' || current == '\r' {
				state = sqlStateNormal
			}
		case sqlStateBlockComment:
			if current == '*' && i+1 < len(source) && source[i+1] == '/' {
				statement.WriteByte(source[i+1])
				i++
				state = sqlStateNormal
			}
		}
	}

	switch state {
	case sqlStateSingleQuoted:
		return nil, fmt.Errorf("unterminated single-quoted string")
	case sqlStateDoubleQuoted:
		return nil, fmt.Errorf("unterminated double-quoted string")
	case sqlStateBacktickQuoted:
		return nil, fmt.Errorf("unterminated backtick-quoted identifier")
	case sqlStateBlockComment:
		return nil, fmt.Errorf("unterminated block comment")
	}

	appendStatement()
	return statements, nil
}

func startsDashComment(source string, dashIndex int) bool {
	if dashIndex+1 >= len(source) || source[dashIndex+1] != '-' {
		return false
	}
	if dashIndex+2 >= len(source) {
		return true
	}

	return source[dashIndex+2] <= ' '
}

func consumeStringByte(
	source string,
	index int,
	quote byte,
	statement *strings.Builder,
	state *sqlScanState,
) int {
	current := source[index]
	if current == '\\' && index+1 < len(source) {
		statement.WriteByte(source[index+1])
		return index + 1
	}
	if current != quote {
		return index
	}
	if index+1 < len(source) && source[index+1] == quote {
		statement.WriteByte(source[index+1])
		return index + 1
	}

	*state = sqlStateNormal
	return index
}

func consumeBacktickIdentifierByte(
	source string,
	index int,
	statement *strings.Builder,
	state *sqlScanState,
) int {
	if source[index] != '`' {
		return index
	}
	if index+1 < len(source) && source[index+1] == '`' {
		statement.WriteByte(source[index+1])
		return index + 1
	}

	*state = sqlStateNormal
	return index
}
