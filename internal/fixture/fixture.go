// Package fixture provisions and resets database fixtures used by weavegate.
package fixture

import (
	"context"
	"database/sql"
)

// Fixture manages the lifecycle of an isolated database fixture.
type Fixture interface {
	Provision(context.Context, FixtureSpec) (*DB, error)
	Reset(context.Context) error
	Teardown(context.Context) error
}

// FixtureSpec describes the database image and SQL sources for a fixture.
type FixtureSpec struct {
	Image      string
	Migrations string
	Seed       string
}

// DB exposes the fixture's managed database connection pool.
//
// A reset may replace SQL while preserving the DB wrapper. Callers must not
// retain the pool or use the fixture concurrently with Reset.
type DB struct {
	SQL *sql.DB
}
