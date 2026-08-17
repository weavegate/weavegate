package fixture

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mysqlcontainer "github.com/testcontainers/testcontainers-go/modules/mysql"
)

var blockingCloseDriverSequence atomic.Uint64

type blockingCloseDriver struct {
	closeStarted chan struct{}
	release      chan struct{}
}

func (d *blockingCloseDriver) Open(string) (driver.Conn, error) {
	return &blockingCloseConn{
		closeStarted: d.closeStarted,
		release:      d.release,
	}, nil
}

type blockingCloseConn struct {
	closeStarted chan struct{}
	release      chan struct{}
	closeOnce    sync.Once
}

func (*blockingCloseConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}

func (c *blockingCloseConn) Close() error {
	c.closeOnce.Do(func() { close(c.closeStarted) })
	<-c.release
	return nil
}

func (*blockingCloseConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported")
}

func (*blockingCloseConn) Ping(context.Context) error { return nil }

func TestMySQLFixtureLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	fixture := NewMySQLFixture()
	t.Cleanup(func() {
		if err := fixture.Teardown(context.Background()); err != nil {
			t.Errorf("cleanup MySQL fixture: %v", err)
		}
	})

	sourceRoot := t.TempDir()
	migrationDir := filepath.Join(sourceRoot, "migration")
	if err := os.Mkdir(migrationDir, 0o755); err != nil {
		t.Fatalf("create migration directory: %v", err)
	}
	copyFixtureSource(t, "testdata/mysql/migration/001_schema.sql", filepath.Join(migrationDir, "001_schema.sql"))
	copyFixtureSource(t, "testdata/mysql/migration/002_quoted_identifier.sql", filepath.Join(migrationDir, "002_quoted_identifier.sql"))
	seedPath := filepath.Join(sourceRoot, "seed.sql")
	copyFixtureSource(t, "testdata/mysql/seed.sql", seedPath)

	spec := FixtureSpec{
		Image:      "mysql:8.4",
		Migrations: migrationDir,
		Seed:       seedPath,
	}
	prepared, err := Prepare(spec)
	if err != nil {
		t.Fatalf("prepare MySQL fixture: %v", err)
	}
	handle, err := fixture.Provision(ctx, prepared)
	if err != nil {
		t.Fatalf("provision MySQL fixture: %v", err)
	}

	if _, err := fixture.Provision(ctx, prepared); err == nil || !strings.Contains(err.Error(), "already provisioned") {
		t.Fatalf("provision twice error = %v, want already provisioned", err)
	}

	provisionRows := itemCount(t, ctx, handle)
	if provisionRows != 1 {
		t.Fatalf("provision row count = %d, want 1", provisionRows)
	}

	var serverUUIDBefore string
	if err := handle.SQL.QueryRowContext(ctx, "SELECT @@server_uuid").Scan(&serverUUIDBefore); err != nil {
		t.Fatalf("read server UUID before reset: %v", err)
	}

	if _, err := handle.SQL.ExecContext(
		ctx,
		"INSERT INTO fixture_item (id, name) VALUES (?, ?)",
		2,
		"mutated",
	); err != nil {
		t.Fatalf("mutate fixture: %v", err)
	}
	if got := itemCount(t, ctx, handle); got != 2 {
		t.Fatalf("mutated row count = %d, want 2", got)
	}
	if err := os.WriteFile(filepath.Join(migrationDir, "001_schema.sql"), []byte("BROKEN EDIT"), 0o644); err != nil {
		t.Fatalf("edit prepared migration source: %v", err)
	}
	if err := os.WriteFile(seedPath, []byte("BROKEN EDIT"), 0o644); err != nil {
		t.Fatalf("edit prepared seed source: %v", err)
	}
	migrationDigest, seedDigest := prepared.MigrationDigest(), prepared.SeedDigest()

	if err := fixture.Reset(ctx); err != nil {
		t.Fatalf("reset MySQL fixture: %v", err)
	}

	resetRows := itemCount(t, ctx, handle)
	if resetRows != 1 {
		t.Fatalf("reset row count = %d, want 1", resetRows)
	}
	if prepared.MigrationDigest() != migrationDigest || prepared.SeedDigest() != seedDigest {
		t.Fatal("prepared digests changed after source edits")
	}

	var serverUUIDAfter string
	if err := handle.SQL.QueryRowContext(ctx, "SELECT @@server_uuid").Scan(&serverUUIDAfter); err != nil {
		t.Fatalf("read server UUID after reset: %v", err)
	}
	if serverUUIDAfter != serverUUIDBefore {
		t.Fatalf("server UUID after reset = %q, want %q", serverUUIDAfter, serverUUIDBefore)
	}

	poolBeforeCanceledReset := handle.SQL
	canceledResetCtx, cancelReset := context.WithCancel(context.Background())
	cancelReset()
	if err := fixture.Reset(canceledResetCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled reset error = %v, want %v", err, context.Canceled)
	}
	if handle.SQL != poolBeforeCanceledReset {
		t.Fatal("canceled reset replaced the application database pool")
	}
	if got := itemCount(t, ctx, handle); got != 1 {
		t.Fatalf("row count after canceled reset = %d, want 1", got)
	}

	if err := fixture.Teardown(ctx); err != nil {
		t.Fatalf("teardown MySQL fixture: %v", err)
	}
	if err := fixture.Teardown(ctx); err != nil {
		t.Fatalf("teardown MySQL fixture twice: %v", err)
	}

	t.Logf(
		"FIXTURE_LIFECYCLE_RESULT image=mysql:8.4 provision_rows=%d reset_rows=%d prepared_snapshot=stable digests=stable teardown=ok",
		provisionRows,
		resetRows,
	)
}

func copyFixtureSource(t *testing.T, source, destination string) {
	t.Helper()
	content, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read fixture source %q: %v", source, err)
	}
	if err := os.WriteFile(destination, content, 0o644); err != nil {
		t.Fatalf("write fixture source %q: %v", destination, err)
	}
}

func TestMySQLFixtureLifecycleErrors(t *testing.T) {
	fixture := NewMySQLFixture()

	if err := fixture.Reset(context.Background()); err == nil || !strings.Contains(err.Error(), "not provisioned") {
		t.Fatalf("reset before provision error = %v, want not provisioned", err)
	}

	if _, err := Prepare(FixtureSpec{}); err == nil || !strings.Contains(err.Error(), "image is required") {
		t.Fatalf("empty image prepare error = %v, want image is required", err)
	}

	_, err := fixture.Provision(context.Background(), Prepared{})
	if err == nil || !strings.Contains(err.Error(), "prepared fixture is required") {
		t.Fatalf("empty prepared fixture error = %v, want prepared fixture is required", err)
	}

	if err := fixture.Teardown(context.Background()); err != nil {
		t.Fatalf("teardown before provision: %v", err)
	}
}

func TestProvisionCleanupUsesIndependentBoundedContext(t *testing.T) {
	operationCtx, cancel := context.WithCancel(context.Background())
	cancel()

	wantErr := errors.New("cleanup failed")
	err := withProvisionCleanupContext(operationCtx, func(cleanupCtx context.Context) error {
		if err := cleanupCtx.Err(); err != nil {
			t.Fatalf("cleanup context error = %v, want active context", err)
		}
		deadline, ok := cleanupCtx.Deadline()
		if !ok {
			t.Fatal("cleanup context has no deadline")
		}
		remaining := time.Until(deadline)
		if remaining <= 0 || remaining > failedProvisionCleanupTimeout {
			t.Fatalf("cleanup context remaining time = %v, want within (0, %v]", remaining, failedProvisionCleanupTimeout)
		}

		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("cleanup error = %v, want %v", err, wantErr)
	}
}

func TestMySQLFixturePreservesFailedProvisionContainerForTeardown(t *testing.T) {
	wantErr := errors.New("terminate failed")
	terminateCalls := 0
	container := &mysqlcontainer.MySQLContainer{}
	fixture := &mysqlFixture{
		terminateContainer: func(context.Context, *mysqlcontainer.MySQLContainer) error {
			terminateCalls++
			if terminateCalls == 1 {
				return wantErr
			}

			return nil
		},
	}

	err := fixture.cleanupFailedProvision(context.Background(), nil, nil, container)
	if !errors.Is(err, wantErr) {
		t.Fatalf("failed provision cleanup error = %v, want %v", err, wantErr)
	}
	if fixture.container != container {
		t.Fatal("failed provision cleanup discarded the container handle")
	}
	if fixture.provisioned {
		t.Fatal("failed provision cleanup marked the fixture as provisioned")
	}
	if _, err := fixture.Provision(context.Background(), Prepared{}); err == nil || !strings.Contains(err.Error(), "cleanup pending") {
		t.Fatalf("provision during pending cleanup error = %v, want cleanup pending", err)
	}

	if err := fixture.Teardown(context.Background()); err != nil {
		t.Fatalf("retry failed provision cleanup: %v", err)
	}
	if fixture.container != nil {
		t.Fatal("successful teardown retained the failed provision container")
	}
	if terminateCalls != 2 {
		t.Fatalf("terminate calls = %d, want 2", terminateCalls)
	}
}

func TestMySQLFixtureTeardownRetriesFailedTermination(t *testing.T) {
	wantErr := errors.New("terminate failed")
	terminateCalls := 0
	fixture := &mysqlFixture{
		container:   &mysqlcontainer.MySQLContainer{},
		db:          &DB{},
		prepared:    Prepared{image: "mysql:8.4", valid: true},
		provisioned: true,
		terminateContainer: func(context.Context, *mysqlcontainer.MySQLContainer) error {
			terminateCalls++
			if terminateCalls == 1 {
				return wantErr
			}

			return nil
		},
	}

	if err := fixture.Teardown(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("first teardown error = %v, want %v", err, wantErr)
	}
	if fixture.container == nil || !fixture.provisioned {
		t.Fatal("failed teardown discarded retryable fixture state")
	}
	if _, err := fixture.Provision(context.Background(), Prepared{}); err == nil || !strings.Contains(err.Error(), "already provisioned") {
		t.Fatalf("provision during pending cleanup error = %v, want already provisioned", err)
	}

	if err := fixture.Teardown(context.Background()); err != nil {
		t.Fatalf("retry teardown: %v", err)
	}
	if fixture.container != nil || fixture.provisioned {
		t.Fatal("successful teardown retained fixture state")
	}
	if err := fixture.Teardown(context.Background()); err != nil {
		t.Fatalf("teardown after cleanup: %v", err)
	}
	if terminateCalls != 2 {
		t.Fatalf("terminate calls = %d, want 2", terminateCalls)
	}
}

func TestMySQLFixtureTeardownHonorsContextDeadline(t *testing.T) {
	terminateCalls := 0
	container := &mysqlcontainer.MySQLContainer{}
	fixture := &mysqlFixture{
		container:   container,
		db:          &DB{},
		prepared:    Prepared{image: "mysql:8.4", valid: true},
		provisioned: true,
		terminateContainer: func(ctx context.Context, got *mysqlcontainer.MySQLContainer) error {
			terminateCalls++
			if got != container {
				t.Fatalf("terminate container = %p, want %p", got, container)
			}
			<-ctx.Done()
			return ctx.Err()
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := fixture.Teardown(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("teardown error = %v, want %v", err, context.DeadlineExceeded)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("teardown elapsed = %v, want bounded by context deadline", elapsed)
	}
	if terminateCalls != 1 {
		t.Fatalf("terminate calls = %d, want 1", terminateCalls)
	}
	if fixture.container != container || !fixture.provisioned {
		t.Fatal("deadline-exceeded teardown discarded retryable fixture state")
	}

	assertMySQLFixtureBoundsPoolClose(t)

	t.Log("FIXTURE_TEARDOWN_CONTEXT_RESULT deadline=honored calls=1 state=retryable pool_close=bounded")
}

func assertMySQLFixtureBoundsPoolClose(t *testing.T) {
	t.Helper()

	closeStarted := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	driverName := fmt.Sprintf("weavegate-blocking-close-%d", blockingCloseDriverSequence.Add(1))
	sql.Register(driverName, &blockingCloseDriver{
		closeStarted: closeStarted,
		release:      release,
	})
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("open blocking-close database: %v", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("ping blocking-close database: %v", err)
	}

	terminateCalls := 0
	fixture := &mysqlFixture{
		container:   &mysqlcontainer.MySQLContainer{},
		db:          &DB{SQL: db},
		prepared:    Prepared{image: "mysql:8.4", valid: true},
		provisioned: true,
		terminateContainer: func(context.Context, *mysqlcontainer.MySQLContainer) error {
			terminateCalls++
			return nil
		},
	}

	started := time.Now()
	err = fixture.Teardown(context.Background())
	elapsed := time.Since(started)
	if err == nil || !strings.Contains(err.Error(), "exceeded 5s teardown budget") {
		t.Fatalf("teardown error = %v, want pool-close budget error", err)
	}
	if elapsed < poolCloseTimeout || elapsed > poolCloseTimeout+time.Second {
		t.Fatalf("teardown elapsed = %v, want within [%v, %v]", elapsed, poolCloseTimeout, poolCloseTimeout+time.Second)
	}
	select {
	case <-closeStarted:
	default:
		t.Fatal("database driver Close was not called")
	}
	if terminateCalls != 1 {
		t.Fatalf("terminate calls = %d, want 1 after bounded pool close", terminateCalls)
	}
	if fixture.container != nil || fixture.provisioned {
		t.Fatal("successful termination retained fixture state after pool-close timeout")
	}
}

func itemCount(t *testing.T, ctx context.Context, db *DB) int {
	t.Helper()

	var count int
	if err := db.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM fixture_item").Scan(&count); err != nil {
		t.Fatalf("count fixture items: %v", err)
	}

	return count
}
