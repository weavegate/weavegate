package fixture

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	mysqlcontainer "github.com/testcontainers/testcontainers-go/modules/mysql"
)

func TestMySQLFixtureLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	fixture := NewMySQLFixture()
	t.Cleanup(func() {
		if err := fixture.Teardown(context.Background()); err != nil {
			t.Errorf("cleanup MySQL fixture: %v", err)
		}
	})

	spec := FixtureSpec{
		Image:      "mysql:8.4",
		Migrations: "testdata/mysql/migration",
		Seed:       "testdata/mysql/seed.sql",
	}
	handle, err := fixture.Provision(ctx, spec)
	if err != nil {
		t.Fatalf("provision MySQL fixture: %v", err)
	}

	if _, err := fixture.Provision(ctx, spec); err == nil || !strings.Contains(err.Error(), "already provisioned") {
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

	if err := fixture.Reset(ctx); err != nil {
		t.Fatalf("reset MySQL fixture: %v", err)
	}

	resetRows := itemCount(t, ctx, handle)
	if resetRows != 1 {
		t.Fatalf("reset row count = %d, want 1", resetRows)
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
		"FIXTURE_LIFECYCLE_RESULT image=mysql:8.4 provision_rows=%d reset_rows=%d teardown=ok",
		provisionRows,
		resetRows,
	)
}

func TestMySQLFixtureLifecycleErrors(t *testing.T) {
	fixture := NewMySQLFixture()

	if err := fixture.Reset(context.Background()); err == nil || !strings.Contains(err.Error(), "not provisioned") {
		t.Fatalf("reset before provision error = %v, want not provisioned", err)
	}

	_, err := fixture.Provision(context.Background(), FixtureSpec{})
	if err == nil || !strings.Contains(err.Error(), "image is required") {
		t.Fatalf("empty image error = %v, want image is required", err)
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
	if _, err := fixture.Provision(context.Background(), FixtureSpec{}); err == nil || !strings.Contains(err.Error(), "cleanup pending") {
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
		spec:        FixtureSpec{Image: "mysql:8.4"},
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
	if _, err := fixture.Provision(context.Background(), FixtureSpec{}); err == nil || !strings.Contains(err.Error(), "already provisioned") {
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

func itemCount(t *testing.T, ctx context.Context, db *DB) int {
	t.Helper()

	var count int
	if err := db.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM fixture_item").Scan(&count); err != nil {
		t.Fatalf("count fixture items: %v", err)
	}

	return count
}
