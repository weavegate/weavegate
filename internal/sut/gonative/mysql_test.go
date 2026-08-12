package gonative

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/weavegate/weavegate/internal/fixture"
	"github.com/weavegate/weavegate/internal/sut"
)

func TestGoNativeMySQL(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	databaseFixture := fixture.NewMySQLFixture()
	t.Cleanup(func() {
		if err := databaseFixture.Teardown(context.Background()); err != nil {
			t.Errorf("cleanup MySQL fixture: %v", err)
		}
	})

	db, err := databaseFixture.Provision(ctx, fixture.FixtureSpec{
		Image:      "mysql:8.4",
		Migrations: filepath.Join("..", "..", "fixture", "testdata", "mysql", "migration"),
		Seed:       filepath.Join("..", "..", "fixture", "testdata", "mysql", "seed.sql"),
	})
	if err != nil {
		t.Fatalf("provision MySQL fixture: %v", err)
	}

	t.Run("runs workers asynchronously on dedicated connections", func(t *testing.T) {
		testDedicatedConnections(t, ctx, db)
	})
	t.Run("stops an active worker and releases its connection", func(t *testing.T) {
		testActiveStop(t, ctx, db)
	})
}

func testDedicatedConnections(
	t *testing.T,
	ctx context.Context,
	db *fixture.DB,
) {
	t.Helper()

	observations := make(chan connectionObservation, 2)
	release := make(chan struct{})
	registry := staticRegistry{
		"probe": func(ctx context.Context, workerID string, conn *sql.Conn) error {
			var connectionID int64
			if err := conn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&connectionID); err != nil {
				return fmt.Errorf("read connection ID: %w", err)
			}

			select {
			case observations <- connectionObservation{workerID: workerID, connectionID: connectionID}:
			case <-ctx.Done():
				return ctx.Err()
			}

			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}

	adapter := New(registry)
	handle, err := adapter.Start(ctx, sut.SUTConfig{}, db)
	if err != nil {
		t.Fatalf("start adapter: %v", err)
	}
	t.Cleanup(func() {
		if err := adapter.Stop(context.Background()); err != nil {
			t.Errorf("cleanup adapter: %v", err)
		}
	})

	results := make(map[string]<-chan sut.WorkerResult, 2)
	for _, workerID := range []string{"w1", "w2"} {
		result, err := handle.Invoke(ctx, workerID, "probe")
		if err != nil {
			t.Fatalf("invoke %s: %v", workerID, err)
		}
		results[workerID] = result
	}

	byWorker := make(map[string]int64, 2)
	for range 2 {
		observation := receiveWithin(t, ctx, observations, "probe observation")
		if _, exists := byWorker[observation.workerID]; exists {
			t.Fatalf("duplicate probe observation for worker %q", observation.workerID)
		}
		byWorker[observation.workerID] = observation.connectionID
	}
	if _, ok := byWorker["w1"]; !ok {
		t.Fatal("probe observations do not contain worker w1")
	}
	if _, ok := byWorker["w2"]; !ok {
		t.Fatal("probe observations do not contain worker w2")
	}
	if byWorker["w1"] == byWorker["w2"] {
		t.Fatalf("worker connection IDs are both %d, want distinct sessions", byWorker["w1"])
	}

	// Both Invoke calls returned and both commands reached this barrier while
	// their result channels remained pending. Releasing the barrier is therefore
	// what lets the asynchronous commands complete; no timing assumption is used.
	for _, workerID := range []string{"w1", "w2"} {
		select {
		case result := <-results[workerID]:
			t.Fatalf("worker %s completed before release with result %+v", workerID, result)
		default:
		}
	}
	close(release)
	for _, workerID := range []string{"w1", "w2"} {
		result := receiveWorkerResult(t, ctx, results[workerID])
		if result.WorkerID != workerID {
			t.Fatalf("result worker ID = %q, want %q", result.WorkerID, workerID)
		}
		if result.Err != nil {
			t.Fatalf("worker %s result error: %v", workerID, result.Err)
		}
		if result.Duration <= 0 {
			t.Fatalf("worker %s duration = %v, want positive", workerID, result.Duration)
		}
	}
	if err := adapter.Stop(ctx); err != nil {
		t.Fatalf("stop adapter: %v", err)
	}
	if got := db.SQL.Stats().InUse; got != 0 {
		t.Fatalf("in-use connections after workers completed = %d, want 0", got)
	}

	t.Log("SUT_ADAPTER_RESULT command=probe workers=2 distinct_connections=2 errors=0 async=true worker_identity=preserved")
}

func testActiveStop(
	t *testing.T,
	ctx context.Context,
	db *fixture.DB,
) {
	t.Helper()

	entered := make(chan string, 1)
	registry := staticRegistry{
		"block": func(ctx context.Context, workerID string, conn *sql.Conn) error {
			var connectionID int64
			if err := conn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&connectionID); err != nil {
				return fmt.Errorf("read connection ID: %w", err)
			}

			select {
			case entered <- workerID:
			case <-ctx.Done():
				return ctx.Err()
			}
			<-ctx.Done()
			return ctx.Err()
		},
	}

	adapter := New(registry)
	handle, err := adapter.Start(ctx, sut.SUTConfig{}, db)
	if err != nil {
		t.Fatalf("start adapter: %v", err)
	}
	results, err := handle.Invoke(ctx, "stop-worker", "block")
	if err != nil {
		t.Fatalf("invoke blocking worker: %v", err)
	}
	if workerID := receiveWithin(t, ctx, entered, "blocking worker entry"); workerID != "stop-worker" {
		t.Fatalf("command worker ID = %q, want stop-worker", workerID)
	}

	stopCtx, cancelStop := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelStop()
	if err := adapter.Stop(stopCtx); err != nil {
		t.Fatalf("stop active adapter: %v", err)
	}

	result := receiveWorkerResult(t, ctx, results)
	if result.WorkerID != "stop-worker" {
		t.Fatalf("stopped result worker ID = %q, want stop-worker", result.WorkerID)
	}
	if !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("stopped result error = %v, want %v", result.Err, context.Canceled)
	}
	if got := db.SQL.Stats().InUse; got != 0 {
		t.Fatalf("in-use connections after Stop = %d, want 0", got)
	}
	if err := adapter.Stop(context.Background()); err != nil {
		t.Fatalf("stop adapter twice: %v", err)
	}
	_, err = handle.Invoke(ctx, "late-worker", "block")
	assertErrorContains(t, err, "stopped")

	t.Log("SUT_STOP_RESULT active_worker=cancelled result_closed=true in_use=0 stop_idempotent=true")
}

type connectionObservation struct {
	workerID     string
	connectionID int64
}

func receiveWorkerResult(
	t *testing.T,
	ctx context.Context,
	results <-chan sut.WorkerResult,
) sut.WorkerResult {
	t.Helper()

	result := receiveWithin(t, ctx, results, "worker result")
	select {
	case _, ok := <-results:
		if ok {
			t.Fatal("worker result channel produced more than one result")
		}
	case <-ctx.Done():
		t.Fatalf("wait for worker result channel close: %v", ctx.Err())
	}
	return result
}

func receiveWithin[T any](
	t *testing.T,
	ctx context.Context,
	values <-chan T,
	description string,
) T {
	t.Helper()

	select {
	case value, ok := <-values:
		if !ok {
			t.Fatalf("%s channel closed without a value", description)
		}
		return value
	case <-ctx.Done():
		t.Fatalf("wait for %s: %v", description, ctx.Err())
		var zero T
		return zero
	}
}
