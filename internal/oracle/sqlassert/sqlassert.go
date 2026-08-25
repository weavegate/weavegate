// Package sqlassert evaluates zero-row SQL invariants as Oracle verdicts.
package sqlassert

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/weavegate/weavegate/internal/oracle"
)

var oracleIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

type zeroRowAssertion struct {
	id    string
	query string
}

// NewZeroRow creates an Oracle that passes only when query returns zero rows.
func NewZeroRow(id, query string) (oracle.Oracle, error) {
	if !oracleIDPattern.MatchString(id) {
		return nil, fmt.Errorf(
			"create zero-row SQL assertion: invalid oracle ID %q: must match %s",
			id,
			oracleIDPattern.String(),
		)
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("create zero-row SQL assertion: query is required")
	}
	return &zeroRowAssertion{id: id, query: query}, nil
}

func (a *zeroRowAssertion) ID() string {
	return a.id
}

func (a *zeroRowAssertion) Evaluate(
	ctx context.Context,
	db oracle.DB,
	_ oracle.RunContext,
) (violations []oracle.Violation, returnErr error) {
	if ctx == nil {
		return nil, fmt.Errorf("evaluate SQL assertion %q: context is required", a.id)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("evaluate SQL assertion %q: context: %w", a.id, err)
	}
	if isNilDB(db) {
		return nil, fmt.Errorf("evaluate SQL assertion %q: database is required", a.id)
	}

	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("evaluate SQL assertion %q: begin read-only transaction: %w", a.id, err)
	}
	if tx == nil {
		return nil, fmt.Errorf("evaluate SQL assertion %q: begin read-only transaction returned nil", a.id)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			violations = nil
			returnErr = errors.Join(
				returnErr,
				fmt.Errorf("evaluate SQL assertion %q: rollback read-only transaction: %w", a.id, rollbackErr),
			)
		}
	}()

	rows, err := tx.QueryContext(ctx, a.query)
	if err != nil {
		return nil, fmt.Errorf("evaluate SQL assertion %q: query: %w", a.id, err)
	}
	var readErr error
	defer func() {
		closeErr := rows.Close()
		if readErr == nil && closeErr == nil {
			return
		}
		var joined error
		if readErr != nil {
			joined = errors.Join(joined, fmt.Errorf("read evidence: %w", readErr))
		}
		if closeErr != nil {
			joined = errors.Join(joined, fmt.Errorf("close rows: %w", closeErr))
		}
		violations = nil
		returnErr = errors.Join(
			returnErr,
			fmt.Errorf("evaluate SQL assertion %q: %w", a.id, joined),
		)
	}()

	evidence, readErr := readRows(rows)
	if readErr != nil {
		// The deferred closer reports the read error together with any close error.
		return
	}

	if len(evidence) == 0 {
		return []oracle.Violation{}, nil
	}
	return []oracle.Violation{
		{OracleID: a.id, Kind: oracle.KindAssertion, Rows: evidence},
	}, nil
}

func isNilDB(db oracle.DB) bool {
	if db == nil {
		return true
	}
	value := reflect.ValueOf(db)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
