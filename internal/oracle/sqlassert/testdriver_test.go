package sqlassert

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

const testDriverName = "weavegate-sqlassert-test"

var (
	registerTestDriver sync.Once
	testConfigs        sync.Map
	testConfigSequence atomic.Uint64
)

type testDriverConfig struct {
	columns       []string
	databaseTypes []string
	rows          [][]driver.Value

	beginErr     error
	queryErr     error
	rowsErr      error
	rowsCloseErr error
	rollbackErr  error
	rejectWrites bool

	mu              sync.Mutex
	beginOptions    []driver.TxOptions
	queries         []string
	rollbackCalls   int
	rowsCloseCalls  int
	connectionClose int
}

func openTestDB(t *testing.T, config *testDriverConfig) *sql.DB {
	t.Helper()
	registerTestDriver.Do(func() {
		sql.Register(testDriverName, &assertionTestDriver{})
	})
	key := fmt.Sprintf("config-%d", testConfigSequence.Add(1))
	testConfigs.Store(key, config)
	t.Cleanup(func() {
		testConfigs.Delete(key)
	})
	db, err := sql.Open(testDriverName, key)
	if err != nil {
		t.Fatalf("open assertion test database: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close assertion test database: %v", err)
		}
	})
	return db
}

func (c *testDriverConfig) snapshot() testDriverSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return testDriverSnapshot{
		beginOptions:    append([]driver.TxOptions(nil), c.beginOptions...),
		queries:         append([]string(nil), c.queries...),
		rollbackCalls:   c.rollbackCalls,
		rowsCloseCalls:  c.rowsCloseCalls,
		connectionClose: c.connectionClose,
	}
}

type testDriverSnapshot struct {
	beginOptions    []driver.TxOptions
	queries         []string
	rollbackCalls   int
	rowsCloseCalls  int
	connectionClose int
}

type assertionTestDriver struct{}

func (*assertionTestDriver) Open(name string) (driver.Conn, error) {
	value, ok := testConfigs.Load(name)
	if !ok {
		return nil, fmt.Errorf("unknown assertion test config %q", name)
	}
	return &assertionTestConn{config: value.(*testDriverConfig)}, nil
}

type assertionTestConn struct {
	config   *testDriverConfig
	readOnly bool
}

func (*assertionTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported by assertion test driver")
}

func (c *assertionTestConn) Close() error {
	c.config.mu.Lock()
	c.config.connectionClose++
	c.config.mu.Unlock()
	return nil
}

func (c *assertionTestConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *assertionTestConn) BeginTx(
	ctx context.Context,
	options driver.TxOptions,
) (driver.Tx, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.config.mu.Lock()
	defer c.config.mu.Unlock()
	if c.config.beginErr != nil {
		return nil, c.config.beginErr
	}
	c.config.beginOptions = append(c.config.beginOptions, options)
	c.readOnly = options.ReadOnly
	return &assertionTestTx{connection: c}, nil
}

func (c *assertionTestConn) QueryContext(
	ctx context.Context,
	query string,
	_ []driver.NamedValue,
) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.config.mu.Lock()
	defer c.config.mu.Unlock()
	c.config.queries = append(c.config.queries, query)
	if c.config.queryErr != nil {
		return nil, c.config.queryErr
	}
	if c.config.rejectWrites && c.readOnly && isMutatingQuery(query) {
		return nil, errors.New("write rejected in read-only transaction")
	}
	rows := make([][]driver.Value, len(c.config.rows))
	for index := range c.config.rows {
		rows[index] = append([]driver.Value(nil), c.config.rows[index]...)
	}
	return &assertionTestRows{
		config:        c.config,
		columns:       append([]string(nil), c.config.columns...),
		databaseTypes: append([]string(nil), c.config.databaseTypes...),
		rows:          rows,
		terminalErr:   c.config.rowsErr,
	}, nil
}

func isMutatingQuery(query string) bool {
	fields := strings.Fields(strings.ToUpper(query))
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "INSERT", "UPDATE", "DELETE", "REPLACE", "CREATE", "ALTER", "DROP", "TRUNCATE":
		return true
	default:
		return false
	}
}

type assertionTestTx struct {
	connection *assertionTestConn
}

func (*assertionTestTx) Commit() error {
	return errors.New("assertion transaction must not commit")
}

func (t *assertionTestTx) Rollback() error {
	t.connection.config.mu.Lock()
	defer t.connection.config.mu.Unlock()
	t.connection.config.rollbackCalls++
	t.connection.readOnly = false
	return t.connection.config.rollbackErr
}

type assertionTestRows struct {
	config        *testDriverConfig
	columns       []string
	databaseTypes []string
	rows          [][]driver.Value
	index         int
	terminalErr   error
	terminalSent  bool
}

func (r *assertionTestRows) Columns() []string {
	return append([]string(nil), r.columns...)
}

func (r *assertionTestRows) Close() error {
	r.config.mu.Lock()
	defer r.config.mu.Unlock()
	r.config.rowsCloseCalls++
	return r.config.rowsCloseErr
}

func (r *assertionTestRows) Next(destination []driver.Value) error {
	if r.index < len(r.rows) {
		row := r.rows[r.index]
		r.index++
		if len(row) != len(destination) {
			return fmt.Errorf("test row columns = %d, want %d", len(row), len(destination))
		}
		copy(destination, row)
		return nil
	}
	if r.terminalErr != nil && !r.terminalSent {
		r.terminalSent = true
		return r.terminalErr
	}
	return io.EOF
}

func (r *assertionTestRows) ColumnTypeDatabaseTypeName(index int) string {
	if index >= len(r.databaseTypes) {
		return ""
	}
	return r.databaseTypes[index]
}
