package gonative

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"

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
		assertErrorContains(t, err, wantErr.Error())
		if results != nil {
			t.Fatal("results channel is non-nil after connection acquisition failure")
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
		assertErrorContains(t, err, context.Canceled.Error())
		if results != nil {
			t.Fatal("results channel is non-nil after cancellation before command start")
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
	_, err := adapter.Invoke(context.Background(), workerID, "command")
	if err == nil {
		t.Fatal("repeat Invoke returned nil error on the failing test connector")
	}
	if strings.Contains(err.Error(), "worker ID is already active") {
		t.Fatalf("repeat Invoke retained worker ID %q: %v", workerID, err)
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
