package fixture

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	mysqlcontainer "github.com/testcontainers/testcontainers-go/modules/mysql"
)

const (
	fixtureDatabase               = "weavegate"
	fixtureUsername               = "weavegate"
	fixturePassword               = "weavegate"
	failedProvisionCleanupTimeout = 30 * time.Second
)

type mysqlFixture struct {
	mu sync.Mutex

	container   *mysqlcontainer.MySQLContainer
	admin       *sql.DB
	db          *DB
	spec        FixtureSpec
	provisioned bool
}

// NewMySQLFixture returns a fixture backed by a Testcontainers MySQL instance.
func NewMySQLFixture() Fixture {
	return &mysqlFixture{}
}

func (f *mysqlFixture) Provision(
	ctx context.Context,
	spec FixtureSpec,
) (_ *DB, returnErr error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.provisioned {
		return nil, fmt.Errorf("provision MySQL fixture: already provisioned")
	}
	if strings.TrimSpace(spec.Image) == "" {
		return nil, fmt.Errorf("provision MySQL fixture: image is required")
	}
	if _, err := migrationSources(spec.Migrations); err != nil {
		return nil, fmt.Errorf("provision MySQL fixture: %w", err)
	}
	if _, err := seedSource(spec.Seed); err != nil {
		return nil, fmt.Errorf("provision MySQL fixture: %w", err)
	}

	container, err := mysqlcontainer.Run(
		ctx,
		spec.Image,
		mysqlcontainer.WithDatabase(fixtureDatabase),
		mysqlcontainer.WithUsername(fixtureUsername),
		mysqlcontainer.WithPassword(fixturePassword),
	)
	if err != nil {
		if container != nil {
			return nil, errors.Join(
				fmt.Errorf("provision MySQL fixture: start container: %w", err),
				cleanupFailedProvision(ctx, nil, nil, container),
			)
		}
		return nil, fmt.Errorf("provision MySQL fixture: start container: %w", err)
	}

	var admin *sql.DB
	var app *sql.DB
	defer func() {
		if returnErr == nil {
			return
		}

		returnErr = errors.Join(
			returnErr,
			cleanupFailedProvision(ctx, app, admin, container),
		)
	}()

	admin, err = openAdminDatabase(ctx, container)
	if err != nil {
		return nil, fmt.Errorf("provision MySQL fixture: %w", err)
	}

	app, err = openApplicationDatabase(ctx, container)
	if err != nil {
		return nil, fmt.Errorf("provision MySQL fixture: %w", err)
	}
	if err := applyFixtureSQL(ctx, app, spec); err != nil {
		return nil, fmt.Errorf("provision MySQL fixture: apply SQL: %w", err)
	}

	handle := &DB{SQL: app}
	f.container = container
	f.admin = admin
	f.db = handle
	f.spec = spec
	f.provisioned = true

	return handle, nil
}

func cleanupFailedProvision(
	operationCtx context.Context,
	app *sql.DB,
	admin *sql.DB,
	container *mysqlcontainer.MySQLContainer,
) error {
	return errors.Join(
		closeDatabase("application database", app),
		closeDatabase("administrative database", admin),
		withProvisionCleanupContext(operationCtx, func(cleanupCtx context.Context) error {
			return terminateContainer(cleanupCtx, container)
		}),
	)
}

func withProvisionCleanupContext(
	operationCtx context.Context,
	cleanup func(context.Context) error,
) error {
	cleanupCtx, cancel := context.WithTimeout(
		context.WithoutCancel(operationCtx),
		failedProvisionCleanupTimeout,
	)
	defer cancel()

	return cleanup(cleanupCtx)
}

func (f *mysqlFixture) Reset(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if !f.provisioned {
		return fmt.Errorf("reset MySQL fixture: not provisioned")
	}

	if err := closeDatabase("application database", f.db.SQL); err != nil {
		return fmt.Errorf("reset MySQL fixture: %w", err)
	}
	f.db.SQL = nil

	if _, err := f.admin.ExecContext(ctx, "DROP DATABASE IF EXISTS `"+fixtureDatabase+"`"); err != nil {
		return fmt.Errorf("reset MySQL fixture: drop database: %w", err)
	}
	if _, err := f.admin.ExecContext(ctx, "CREATE DATABASE `"+fixtureDatabase+"`"); err != nil {
		return fmt.Errorf("reset MySQL fixture: create database: %w", err)
	}

	app, err := openApplicationDatabase(ctx, f.container)
	if err != nil {
		return fmt.Errorf("reset MySQL fixture: %w", err)
	}
	if err := applyFixtureSQL(ctx, app, f.spec); err != nil {
		return errors.Join(
			fmt.Errorf("reset MySQL fixture: apply SQL: %w", err),
			closeDatabase("application database", app),
		)
	}

	f.db.SQL = app
	return nil
}

func (f *mysqlFixture) Teardown(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if !f.provisioned && f.container == nil && f.admin == nil && f.db == nil {
		return nil
	}

	var app *sql.DB
	if f.db != nil {
		app = f.db.SQL
		f.db.SQL = nil
	}

	err := errors.Join(
		closeDatabase("application database", app),
		closeDatabase("administrative database", f.admin),
		terminateContainer(ctx, f.container),
	)

	f.container = nil
	f.admin = nil
	f.db = nil
	f.spec = FixtureSpec{}
	f.provisioned = false

	return err
}

func openAdminDatabase(
	ctx context.Context,
	container *mysqlcontainer.MySQLContainer,
) (*sql.DB, error) {
	endpoint, err := container.PortEndpoint(ctx, "3306/tcp", "")
	if err != nil {
		return nil, fmt.Errorf("get administrative endpoint: %w", err)
	}

	config := mysqldriver.NewConfig()
	config.User = "root"
	config.Passwd = fixturePassword
	config.Net = "tcp"
	config.Addr = endpoint

	db, err := sql.Open("mysql", config.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("open administrative database: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		return nil, errors.Join(
			fmt.Errorf("ping administrative database: %w", err),
			closeDatabase("administrative database", db),
		)
	}

	return db, nil
}

func openApplicationDatabase(
	ctx context.Context,
	container *mysqlcontainer.MySQLContainer,
) (*sql.DB, error) {
	dsn, err := container.ConnectionString(ctx, "parseTime=true")
	if err != nil {
		return nil, fmt.Errorf("get application connection string: %w", err)
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open application database: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		return nil, errors.Join(
			fmt.Errorf("ping application database: %w", err),
			closeDatabase("application database", db),
		)
	}

	return db, nil
}

func closeDatabase(name string, db *sql.DB) error {
	if db == nil {
		return nil
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("close %s: %w", name, err)
	}

	return nil
}

func terminateContainer(
	ctx context.Context,
	container *mysqlcontainer.MySQLContainer,
) error {
	if container == nil {
		return nil
	}
	if err := container.Terminate(ctx); err != nil {
		return fmt.Errorf("terminate MySQL container: %w", err)
	}

	return nil
}
