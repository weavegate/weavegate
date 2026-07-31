package lostupdate

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"strings"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	mysqlcontainer "github.com/testcontainers/testcontainers-go/modules/mysql"
)

//go:embed schema.sql
var schemaSQL string

func startMySQL(t *testing.T) *sql.DB {
	t.Helper()

	// Start an isolated MySQL instance for the experiment.
	ctx := context.Background()
	container, err := mysqlcontainer.Run(
		ctx,
		"mysql:8.4",
		mysqlcontainer.WithDatabase("app"),
		mysqlcontainer.WithUsername("app"),
		mysqlcontainer.WithPassword("app"),
	)
	if err != nil {
		t.Fatalf("start MySQL: %v", err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(context.Background()); err != nil {
			t.Errorf("terminate MySQL: %v", err)
		}
	})

	// Open the database handle used by the experiment.
	dsn, err := container.ConnectionString(ctx, "parseTime=true")
	if err != nil {
		t.Fatalf("get MySQL connection string: %v", err)
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open MySQL: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close MySQL: %v", err)
		}
	})

	// Initialize the shared account row from the embedded schema.
	if err := applySchema(ctx, db); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	return db
}

func applySchema(ctx context.Context, db *sql.DB) error {
	// Execute each non-empty statement in declaration order.
	for index, statement := range strings.Split(schemaSQL, ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}

		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("execute schema statement %d: %w", index+1, err)
		}
	}

	return nil
}
