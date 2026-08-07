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

	statementNumber := 0
	for _, rawStatement := range strings.Split(string(contents), ";") {
		statement := strings.TrimSpace(rawStatement)
		if statement == "" {
			continue
		}

		statementNumber++
		if _, err := executor.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf(
				"execute %s file %q statement %d: %w",
				source.phase,
				filepath.Base(source.path),
				statementNumber,
				err,
			)
		}
	}

	return nil
}
