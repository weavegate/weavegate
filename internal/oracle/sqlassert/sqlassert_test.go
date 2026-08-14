package sqlassert

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/weavegate/weavegate/internal/oracle"
)

const matchingAssertionQuery = `
SELECT
    project_request_id,
    COUNT(*) AS active_assignment_count
FROM assignment
WHERE status = 'ACTIVE'
GROUP BY project_request_id
HAVING COUNT(*) > 1
ORDER BY project_request_id;
`

func TestSQLAssertionReturnsCanonicalViolationAndPass(t *testing.T) {
	config := &testDriverConfig{
		columns:       []string{"project_request_id", "active_assignment_count"},
		databaseTypes: []string{"BIGINT", "BIGINT"},
		rows: [][]driver.Value{
			{[]byte("42"), []byte("2")},
		},
	}
	db := openTestDB(t, config)
	assertion, err := NewZeroRow("active-assignment-is-unique", matchingAssertionQuery)
	if err != nil {
		t.Fatalf("create SQL assertion: %v", err)
	}
	violations, err := assertion.Evaluate(context.Background(), db, oracle.RunContext{})
	if err != nil {
		t.Fatalf("evaluate violating assertion: %v", err)
	}
	if len(violations) != 1 || violations[0].OracleID != "active-assignment-is-unique" ||
		violations[0].Kind != oracle.KindAssertion || len(violations[0].Rows) != 1 {
		t.Fatalf("violations = %#v, want one assertion violation with one row", violations)
	}
	evidenceJSON, err := json.Marshal(violations[0].Rows[0])
	if err != nil {
		t.Fatalf("marshal evidence: %v", err)
	}
	const wantEvidence = `{"active_assignment_count":2,"project_request_id":42}`
	if string(evidenceJSON) != wantEvidence {
		t.Fatalf("evidence JSON = %s, want %s", evidenceJSON, wantEvidence)
	}
	observed := config.snapshot()
	if len(observed.beginOptions) != 1 || !observed.beginOptions[0].ReadOnly {
		t.Fatalf("transaction options = %#v, want one read-only transaction", observed.beginOptions)
	}
	if observed.rollbackCalls != 1 || observed.rowsCloseCalls == 0 {
		t.Fatalf("cleanup = rollback:%d rows_close:%d, want 1/at least 1", observed.rollbackCalls, observed.rowsCloseCalls)
	}
	if len(observed.queries) != 1 || !strings.Contains(observed.queries[0], "HAVING COUNT(*) > 1") {
		t.Fatalf("queries = %#v, want canonical matching assertion", observed.queries)
	}

	passConfig := &testDriverConfig{
		columns:       []string{"project_request_id", "active_assignment_count"},
		databaseTypes: []string{"BIGINT", "BIGINT"},
	}
	passDB := openTestDB(t, passConfig)
	pass, err := assertion.Evaluate(context.Background(), passDB, oracle.RunContext{})
	if err != nil {
		t.Fatalf("evaluate passing assertion: %v", err)
	}
	if pass == nil || len(pass) != 0 {
		t.Fatalf("PASS violations = %#v, want non-nil empty", pass)
	}

	t.Log(
		"SQL_ASSERT_ORACLE_RESULT id=active-assignment-is-unique expect_rows=0 " +
			"observed_rows=1 violations=1 evidence_json=" + wantEvidence +
			" invalid_utf8=error scan_error=error",
	)
}

func TestSQLAssertionSortsEvidenceAndRejectsColumns(t *testing.T) {
	config := &testDriverConfig{
		columns:       []string{"key", "count"},
		databaseTypes: []string{"VARCHAR", "BIGINT"},
		rows: [][]driver.Value{
			{[]byte("z"), []byte("2")},
			{[]byte("a"), []byte("1")},
		},
	}
	db := openTestDB(t, config)
	assertion := mustZeroRow(t, "sorted-evidence", "SELECT key, count FROM evidence")
	violations, err := assertion.Evaluate(context.Background(), db, oracle.RunContext{})
	if err != nil {
		t.Fatalf("evaluate sorted evidence: %v", err)
	}
	if got := violations[0].Rows[0]["key"]; got != "a" {
		t.Fatalf("first canonical evidence key = %v, want a", got)
	}

	tests := []struct {
		name    string
		columns []string
	}{
		{name: "blank", columns: []string{" "}},
		{name: "duplicate", columns: []string{"same", "same"}},
		{name: "invalid UTF-8", columns: []string{string([]byte{0xff})}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			row := make([]driver.Value, len(test.columns))
			types := make([]string, len(test.columns))
			invalidConfig := &testDriverConfig{
				columns:       test.columns,
				databaseTypes: types,
				rows:          [][]driver.Value{row},
			}
			invalidDB := openTestDB(t, invalidConfig)
			invalid := mustZeroRow(t, "invalid-columns", "SELECT invalid")
			if _, err := invalid.Evaluate(context.Background(), invalidDB, oracle.RunContext{}); err == nil {
				t.Fatal("invalid columns evaluation error = nil")
			}
			observed := invalidConfig.snapshot()
			if observed.rowsCloseCalls == 0 || observed.rollbackCalls != 1 {
				t.Fatalf("invalid-column cleanup = rows_close:%d rollback:%d", observed.rowsCloseCalls, observed.rollbackCalls)
			}
		})
	}
}

func TestSQLAssertionRejectsInvalidConstructorAndInputs(t *testing.T) {
	for _, id := range []string{"", "UPPER", "-leading", "has_underscore"} {
		if _, err := NewZeroRow(id, "SELECT 1"); err == nil {
			t.Fatalf("invalid ID %q error = nil", id)
		}
	}
	if _, err := NewZeroRow("valid-id", " \n\t "); err == nil {
		t.Fatal("blank query error = nil")
	}
	if _, err := NewZeroRow("trailing-", "SELECT 1"); err != nil {
		t.Fatalf("valid trailing-hyphen ID rejected: %v", err)
	}

	assertion := mustZeroRow(t, "input-check", "SELECT 1")
	if _, err := assertion.Evaluate(nil, nil, oracle.RunContext{}); err == nil {
		t.Fatal("nil context error = nil")
	}
	if _, err := assertion.Evaluate(context.Background(), nil, oracle.RunContext{}); err == nil {
		t.Fatal("nil DB error = nil")
	}
	var typedNil *typedNilDB
	if _, err := assertion.Evaluate(context.Background(), typedNil, oracle.RunContext{}); err == nil {
		t.Fatal("typed nil DB error = nil")
	}
	nilTransactionDB := beginDBFunc(func(
		_ context.Context,
		options *sql.TxOptions,
	) (*sql.Tx, error) {
		if options == nil || !options.ReadOnly {
			t.Fatalf("nil-transaction BeginTx options = %#v, want read-only", options)
		}
		return nil, nil
	})
	if _, err := assertion.Evaluate(context.Background(), nilTransactionDB, oracle.RunContext{}); err == nil ||
		!strings.Contains(err.Error(), "returned nil") {
		t.Fatalf("nil transaction error = %v", err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	config := &testDriverConfig{}
	db := openTestDB(t, config)
	if _, err := assertion.Evaluate(canceled, db, oracle.RunContext{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled evaluation error = %v, want context.Canceled", err)
	}
	if got := len(config.snapshot().beginOptions); got != 0 {
		t.Fatalf("canceled evaluation began %d transaction(s), want 0", got)
	}
}

func TestNormalizeDriverValues(t *testing.T) {
	local := time.Date(2026, time.August, 14, 9, 30, 0, 123, time.FixedZone("KST", 9*60*60))
	tests := []struct {
		name         string
		value        any
		databaseType string
		want         any
	}{
		{name: "nil", value: nil, want: nil},
		{name: "bool", value: true, want: true},
		{name: "int64", value: int64(-42), want: int64(-42)},
		{name: "uint64", value: uint64(42), want: uint64(42)},
		{name: "float64", value: float64(1.25), want: float64(1.25)},
		{name: "string", value: "안전", want: "안전"},
		{name: "time UTC", value: local, want: "2026-08-14T00:30:00.000000123Z"},
		{name: "signed bytes", value: []byte("-42"), databaseType: "BIGINT", want: int64(-42)},
		{name: "count bytes", value: []byte("2"), databaseType: "BIGINT", want: int64(2)},
		{name: "unsigned bytes", value: []byte("18446744073709551615"), databaseType: "BIGINT UNSIGNED", want: uint64(math.MaxUint64)},
		{name: "unsigned width bytes", value: []byte("42"), databaseType: "INT(11) UNSIGNED", want: uint64(42)},
		{name: "text bytes", value: []byte("hello"), databaseType: "VARCHAR", want: "hello"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeValue(test.value, test.databaseType)
			if err != nil {
				t.Fatalf("normalize value: %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("normalized value = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestNormalizeRejectsUnsupportedDriverValues(t *testing.T) {
	invalidUTF8 := []byte{0xff}
	tests := []struct {
		name         string
		value        any
		databaseType string
	}{
		{name: "signed overflow", value: []byte("9223372036854775808"), databaseType: "BIGINT"},
		{name: "unsigned negative", value: []byte("-1"), databaseType: "BIGINT UNSIGNED"},
		{name: "unsigned overflow", value: []byte("18446744073709551616"), databaseType: "BIGINT UNSIGNED"},
		{name: "integer syntax", value: []byte("2.0"), databaseType: "BIGINT"},
		{name: "invalid UTF-8 bytes", value: invalidUTF8, databaseType: "VARCHAR"},
		{name: "invalid UTF-8 string", value: string(invalidUTF8)},
		{name: "NaN", value: math.NaN()},
		{name: "positive infinity", value: math.Inf(1)},
		{name: "negative infinity", value: math.Inf(-1)},
		{name: "plain int", value: int(1)},
		{name: "nested slice", value: []string{"nested"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := normalizeValue(test.value, test.databaseType); err == nil {
				t.Fatal("invalid driver value error = nil")
			}
		})
	}
}

func TestReadOnlyTransactionRejectsMutation(t *testing.T) {
	config := &testDriverConfig{rejectWrites: true}
	db := openTestDB(t, config)
	assertion := mustZeroRow(t, "mutation-check", "INSERT INTO evidence(value) VALUES (1)")
	_, err := assertion.Evaluate(context.Background(), db, oracle.RunContext{})
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("mutating assertion error = %v, want read-only rejection", err)
	}
	observed := config.snapshot()
	if len(observed.beginOptions) != 1 || !observed.beginOptions[0].ReadOnly || observed.rollbackCalls != 1 {
		t.Fatalf("read-only cleanup = %#v", observed)
	}
}

func TestCleanupPreservesQueryAndRollbackErrors(t *testing.T) {
	queryErr := errors.New("query failed")
	rollbackErr := errors.New("rollback failed")
	config := &testDriverConfig{queryErr: queryErr, rollbackErr: rollbackErr}
	db := openTestDB(t, config)
	assertion := mustZeroRow(t, "cleanup-query", "SELECT value FROM evidence")
	violations, err := assertion.Evaluate(context.Background(), db, oracle.RunContext{})
	if !errors.Is(err, queryErr) || !errors.Is(err, rollbackErr) {
		t.Fatalf("cleanup error = %v, want query and rollback causes", err)
	}
	if violations != nil {
		t.Fatalf("query failure violations = %#v, want nil", violations)
	}
	if config.snapshot().rollbackCalls != 1 {
		t.Fatalf("rollback calls = %d, want 1", config.snapshot().rollbackCalls)
	}
}

func TestCleanupRejectsScanRowsCloseAndRollbackFailures(t *testing.T) {
	scanErrConfig := &testDriverConfig{
		columns:       []string{"value"},
		databaseTypes: []string{"INT"},
		rows:          [][]driver.Value{{int(1)}},
	}
	assertEvaluationError(t, "scan", scanErrConfig, "scan")

	rowsErr := errors.New("rows failed")
	rowsErrConfig := &testDriverConfig{
		columns:       []string{"value"},
		databaseTypes: []string{"INT"},
		rowsErr:       rowsErr,
	}
	assertEvaluationCause(t, "rows", rowsErrConfig, rowsErr)

	closeErr := errors.New("rows close failed")
	closeErrConfig := &testDriverConfig{
		columns:       []string{"value"},
		databaseTypes: []string{"INT"},
		rowsCloseErr:  closeErr,
	}
	assertEvaluationCause(t, "close", closeErrConfig, closeErr)

	rollbackErr := errors.New("rollback after read failed")
	rollbackConfig := &testDriverConfig{
		columns:       []string{"value"},
		databaseTypes: []string{"INT"},
		rows:          [][]driver.Value{{int64(1)}},
		rollbackErr:   rollbackErr,
	}
	assertEvaluationCause(t, "rollback", rollbackConfig, rollbackErr)

	beginErr := errors.New("begin failed")
	beginConfig := &testDriverConfig{beginErr: beginErr}
	assertEvaluationCause(t, "begin", beginConfig, beginErr)
	if beginConfig.snapshot().rollbackCalls != 0 {
		t.Fatal("failed BeginTx attempted rollback")
	}
}

func mustZeroRow(t *testing.T, id, query string) oracle.Oracle {
	t.Helper()
	assertion, err := NewZeroRow(id, query)
	if err != nil {
		t.Fatalf("create zero-row assertion: %v", err)
	}
	return assertion
}

func assertEvaluationError(
	t *testing.T,
	name string,
	config *testDriverConfig,
	wantSubstring string,
) {
	t.Helper()
	db := openTestDB(t, config)
	assertion := mustZeroRow(t, "error-"+name, "SELECT value FROM evidence")
	violations, err := assertion.Evaluate(context.Background(), db, oracle.RunContext{})
	if err == nil || !strings.Contains(err.Error(), wantSubstring) {
		t.Fatalf("%s evaluation error = %v, want substring %q", name, err, wantSubstring)
	}
	if violations != nil {
		t.Fatalf("%s evaluation violations = %#v, want nil", name, violations)
	}
	if config.beginErr == nil && config.snapshot().rollbackCalls != 1 {
		t.Fatalf("%s rollback calls = %d, want 1", name, config.snapshot().rollbackCalls)
	}
}

func assertEvaluationCause(t *testing.T, name string, config *testDriverConfig, want error) {
	t.Helper()
	db := openTestDB(t, config)
	assertion := mustZeroRow(t, "error-"+name, "SELECT value FROM evidence")
	violations, err := assertion.Evaluate(context.Background(), db, oracle.RunContext{})
	if !errors.Is(err, want) {
		t.Fatalf("%s evaluation error = %v, want errors.Is(_, %v)", name, err, want)
	}
	if violations != nil {
		t.Fatalf("%s evaluation violations = %#v, want nil", name, violations)
	}
	if config.beginErr == nil && config.snapshot().rollbackCalls != 1 {
		t.Fatalf("%s rollback calls = %d, want 1", name, config.snapshot().rollbackCalls)
	}
}

type typedNilDB struct{}

func (*typedNilDB) BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error) {
	panic("typed nil DB must be rejected before BeginTx")
}

type beginDBFunc func(context.Context, *sql.TxOptions) (*sql.Tx, error)

func (f beginDBFunc) BeginTx(ctx context.Context, options *sql.TxOptions) (*sql.Tx, error) {
	return f(ctx, options)
}
