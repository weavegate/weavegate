package gonative

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/weavegate/weavegate/internal/fixture"
)

func TestGoNativeUnstartedWorkerCleanup(t *testing.T) {
	t.Run("connection acquisition failure", func(t *testing.T) {
		wantErr := errors.New("connect failed")
		adapter, db, connector, captured := newCleanupTestAdapter(t)
		connector.connect = func(context.Context) (driver.Conn, error) {
			captureActiveWorker(t, adapter, captured, "worker")
			return nil, wantErr
		}

		results, err := adapter.Invoke(context.Background(), "worker", "command")
		if err != nil {
			t.Fatalf("invoke: %v", err)
		}
		outcome := <-results
		if outcome.Worker != nil || outcome.Unstarted == nil || !errors.Is(outcome.Unstarted.Err, wantErr) {
			t.Fatalf("unstarted outcome = %#v", outcome)
		}
		assertUnstartedWorkerCompleted(t, adapter, <-captured)
		assertWorkerIDReusable(t, adapter, "worker")

		if err := db.Close(); err != nil {
			t.Fatalf("close database: %v", err)
		}
	})

	t.Run("cancellation after connection acquisition", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		adapter, db, connector, captured := newCleanupTestAdapter(t)
		connector.connect = func(context.Context) (driver.Conn, error) {
			captureActiveWorker(t, adapter, captured, "worker")
			cancel()
			return cleanupTestConn{}, nil
		}

		results, err := adapter.Invoke(ctx, "worker", "command")
		if err != nil {
			t.Fatalf("invoke: %v", err)
		}
		outcome := <-results
		if outcome.Worker != nil || outcome.Unstarted == nil || !errors.Is(outcome.Unstarted.Err, context.Canceled) {
			t.Fatalf("unstarted outcome = %#v", outcome)
		}
		worker := <-captured
		assertUnstartedWorkerCompleted(t, adapter, worker)
		if worker.cleanupErr != nil {
			t.Fatalf("cleanup error = %v, want nil", worker.cleanupErr)
		}
		assertWorkerIDReusable(t, adapter, "worker")

		if err := db.Close(); err != nil {
			t.Fatalf("close database: %v", err)
		}
	})
}

func newCleanupTestAdapter(t *testing.T) (*adapter, *sql.DB, *cleanupTestConnector, chan *activeWorker) {
	t.Helper()

	connector := &cleanupTestConnector{}
	db := sql.OpenDB(connector)
	captured := make(chan *activeWorker, 1)
	adapter := &adapter{
		state: adapterStateStarted,
		db:    &fixture.DB{SQL: db},
		commands: map[string]CommandFunc{
			"command": func(context.Context, string, *sql.Conn) error {
				t.Fatal("command started on an unstarted-worker cleanup path")
				return nil
			},
		},
		active: make(map[string]*activeWorker),
	}
	return adapter, db, connector, captured
}

func captureActiveWorker(
	t *testing.T,
	adapter *adapter,
	captured chan<- *activeWorker,
	workerID string,
) {
	t.Helper()

	adapter.mu.Lock()
	worker := adapter.active[workerID]
	adapter.mu.Unlock()
	if worker == nil {
		t.Fatalf("active worker %q was not reserved before connection acquisition", workerID)
	}
	captured <- worker
}

func assertUnstartedWorkerCompleted(t *testing.T, adapter *adapter, worker *activeWorker) {
	t.Helper()

	if _, ok := <-worker.results; ok {
		t.Fatal("results channel is open, want terminal closed state")
	}
	if _, ok := <-worker.done; ok {
		t.Fatal("done channel is open, want closed state")
	}
	adapter.mu.Lock()
	_, active := adapter.active[worker.workerID]
	adapter.mu.Unlock()
	if active {
		t.Fatalf("worker %q remains active after cleanup", worker.workerID)
	}
}

func assertWorkerIDReusable(t *testing.T, adapter *adapter, workerID string) {
	t.Helper()

	closedDB := sql.OpenDB(&cleanupTestConnector{})
	if err := closedDB.Close(); err != nil {
		t.Fatalf("close reuse-check database: %v", err)
	}
	adapter.db.SQL = closedDB
	results, err := adapter.Invoke(context.Background(), workerID, "command")
	if err != nil && strings.Contains(err.Error(), "worker ID is already active") {
		t.Fatalf("repeat Invoke retained worker ID %q: %v", workerID, err)
	}
	if err != nil {
		t.Fatalf("repeat Invoke: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	outcome := receiveWithin(t, ctx, results, "reused worker outcome")
	if outcome.Unstarted == nil {
		t.Fatal("expected unstarted outcome on closed DB")
	}
	if _, ok := <-results; ok {
		t.Fatal("expected stream closure")
	}
}

type cleanupTestConnector struct {
	connect func(context.Context) (driver.Conn, error)
}

func (c *cleanupTestConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if c.connect == nil {
		return nil, errors.New("cleanup test connector is closed")
	}
	return c.connect(ctx)
}

func (*cleanupTestConnector) Driver() driver.Driver {
	return cleanupTestDriver{}
}

type cleanupTestDriver struct{}

func (cleanupTestDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("cleanup test driver must be opened through its connector")
}

type cleanupTestConn struct{}

func (cleanupTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}

func (cleanupTestConn) Close() error {
	return nil
}

func (cleanupTestConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported")
}

func (cleanupTestConn) Ping(context.Context) error {
	return io.EOF
}

func TestGoNativeAsyncUnstarted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	adapter, db, connector, captured := newCleanupTestAdapter(t)
	t.Cleanup(func() { _ = db.Close() })
	entered := make(chan struct{})
	connector.connect = func(ctx context.Context) (driver.Conn, error) {
		captureActiveWorker(t, adapter, captured, "worker")
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	workerCtx, cancelWorker := context.WithCancel(ctx)
	defer cancelWorker()
	stream, err := adapter.Invoke(workerCtx, "worker", "command")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if duplicate, err := adapter.Invoke(ctx, "worker", "command"); err == nil || duplicate != nil {
		t.Fatal("worker reservation released before asynchronous outcome")
	}
	cancelWorker()
	outcome := receiveWithin(t, ctx, stream, "unstarted outcome")
	if outcome.Worker != nil || outcome.Unstarted == nil || !errors.Is(outcome.Unstarted.Err, context.Canceled) {
		t.Fatalf("outcome = %#v", outcome)
	}
	assertUnstartedWorkerCompleted(t, adapter, <-captured)
	assertWorkerIDReusable(t, adapter, "worker")
	if adapter.Faults().Err() != nil {
		t.Fatalf("unstarted invocation became session failure: %v", adapter.Faults().Err())
	}
	t.Log("SUT_ASYNC_UNSTARTED_RESULT invoke=nonblocking reservation=held cancellation=preserved resources=returned stream=closed worker_id=reusable")
}

func TestGoNativeCleanupSessionFault(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	adapter, db, connector, _ := newCleanupTestAdapter(t)
	t.Cleanup(func() { _ = db.Close() })
	connector.connect = func(context.Context) (driver.Conn, error) { return cleanupTestConn{}, nil }
	// A broken command returns its lease itself. The adapter can no longer
	// establish its own required successful connection-return boundary.
	adapter.commands["command"] = func(_ context.Context, _ string, conn *sql.Conn) error { return conn.Close() }
	stream, err := adapter.Invoke(ctx, "worker", "command")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case outcome, ok := <-stream:
		if ok {
			t.Fatalf("cleanup fault fabricated outcome: %#v", outcome)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if !errors.Is(adapter.Faults().Err(), sql.ErrConnDone) {
		t.Fatalf("fault = %v", adapter.Faults().Err())
	}
	if next, err := adapter.Invoke(ctx, "other", "command"); next != nil || !errors.Is(err, sql.ErrConnDone) {
		t.Fatalf("faulted session accepted invocation: %v", err)
	}
	if err := adapter.Stop(ctx); !errors.Is(err, sql.ErrConnDone) {
		t.Fatalf("Stop lost retired worker cleanup cause: %v", err)
	}
}
