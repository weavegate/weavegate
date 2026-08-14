// Package oracle defines post-run verdict contracts that are independent from
// fixture provisioning and SUT execution details.
package oracle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"regexp"

	"github.com/weavegate/weavegate/internal/trace"
)

var oracleIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// DB is the minimum database capability available to an Oracle.
type DB interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

// Row is deterministic, JSON-safe evidence keyed by a stable column name.
type Row map[string]any

// Snapshot reserves named golden projections for later differential Oracles.
type Snapshot struct {
	Projections map[string][]Row
}

// RunContext is the normalized post-run evidence available to an Oracle. Raw
// worker errors, durations, and wall-clock values are deliberately absent.
type RunContext struct {
	Golden    *Snapshot
	Trace     trace.Trace
	Terminals trace.Terminals
}

// Kind classifies a violation independently from future diagnostic codes.
type Kind string

const (
	// KindAssertion identifies a failed invariant assertion.
	KindAssertion Kind = "assertion"
)

// Violation is one Oracle verdict with deterministic evidence rows.
type Violation struct {
	OracleID string `json:"oracle_id"`
	Kind     Kind   `json:"kind"`
	Rows     []Row  `json:"rows"`
}

// Oracle evaluates one post-run invariant.
type Oracle interface {
	ID() string
	Evaluate(context.Context, DB, RunContext) ([]Violation, error)
}

// Evaluator composes post-run invariant evaluations.
type Evaluator interface {
	Evaluate(context.Context, DB, RunContext) (Evaluation, error)
}

// EvaluatorFunc adapts a function for orchestrator protocol tests.
type EvaluatorFunc func(context.Context, DB, RunContext) (Evaluation, error)

// Evaluate calls f or returns an explicit error for a nil function.
func (f EvaluatorFunc) Evaluate(
	ctx context.Context,
	db DB,
	run RunContext,
) (Evaluation, error) {
	if f == nil {
		return Evaluation{}, errors.New("evaluate oracles: evaluator function is nil")
	}
	return f(ctx, db, run)
}

type setEntry struct {
	id     string
	oracle Oracle
}

// Set evaluates a validated, declaration-ordered collection of Oracles.
type Set struct {
	entries []setEntry
}

// NewSet validates the complete Oracle collection before any run or database
// access can begin.
func NewSet(oracles ...Oracle) (*Set, error) {
	if len(oracles) == 0 {
		return nil, errors.New("create oracle set: at least one oracle is required")
	}

	entries := make([]setEntry, 0, len(oracles))
	seen := make(map[string]struct{}, len(oracles))
	for index, value := range oracles {
		if isNilOracle(value) {
			return nil, fmt.Errorf("create oracle set: oracle[%d] is nil", index)
		}
		id := value.ID()
		if err := validateOracleID(id); err != nil {
			return nil, fmt.Errorf("create oracle set: oracle[%d]: %w", index, err)
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("create oracle set: duplicate oracle ID %q", id)
		}
		seen[id] = struct{}{}
		entries = append(entries, setEntry{id: id, oracle: value})
	}
	return &Set{entries: entries}, nil
}

// Evaluate runs every Oracle in declaration order and preserves a result even
// when that Oracle passes with zero violations.
func (s *Set) Evaluate(
	ctx context.Context,
	db DB,
	run RunContext,
) (Evaluation, error) {
	if s == nil || len(s.entries) == 0 {
		return Evaluation{}, errors.New("evaluate oracle set: set is empty")
	}
	if ctx == nil {
		return Evaluation{}, errors.New("evaluate oracle set: context is required")
	}

	results := make([]OracleResult, 0, len(s.entries))
	for _, entry := range s.entries {
		if isNilOracle(entry.oracle) {
			return Evaluation{}, fmt.Errorf("evaluate oracle %q: oracle is nil", entry.id)
		}
		if currentID := entry.oracle.ID(); currentID != entry.id {
			return Evaluation{}, fmt.Errorf(
				"evaluate oracle %q: stable ID changed to %q",
				entry.id,
				currentID,
			)
		}

		oracleRun, err := cloneRunContext(run)
		if err != nil {
			return Evaluation{}, fmt.Errorf("evaluate oracle %q: clone run context: %w", entry.id, err)
		}
		violations, err := entry.oracle.Evaluate(ctx, db, oracleRun)
		if err != nil {
			return Evaluation{}, fmt.Errorf("evaluate oracle %q: %w", entry.id, err)
		}
		result, err := normalizeOracleResult(OracleResult{
			OracleID:   entry.id,
			Violations: violations,
		})
		if err != nil {
			return Evaluation{}, fmt.Errorf("evaluate oracle %q: invalid result: %w", entry.id, err)
		}
		results = append(results, result)
	}
	if len(results) != len(s.entries) {
		return Evaluation{}, fmt.Errorf(
			"evaluate oracle set: got %d results for %d oracles",
			len(results),
			len(s.entries),
		)
	}

	evaluation, err := NewEvaluation(results...)
	if err != nil {
		return Evaluation{}, fmt.Errorf("evaluate oracle set: build evaluation: %w", err)
	}
	return evaluation, nil
}

func validateOracleID(id string) error {
	if !oracleIDPattern.MatchString(id) {
		return fmt.Errorf(
			"invalid oracle ID %q: must match %s",
			id,
			oracleIDPattern.String(),
		)
	}
	return nil
}

func isNilOracle(value Oracle) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func cloneRunContext(run RunContext) (RunContext, error) {
	cloned := RunContext{
		Trace:     run.Trace.Clone(),
		Terminals: run.Terminals.Clone(),
	}
	if run.Golden == nil {
		return cloned, nil
	}

	cloned.Golden = &Snapshot{}
	if run.Golden.Projections == nil {
		return cloned, nil
	}
	cloned.Golden.Projections = make(map[string][]Row, len(run.Golden.Projections))
	for name, rows := range run.Golden.Projections {
		clonedRows, err := cloneRows(rows)
		if err != nil {
			return RunContext{}, fmt.Errorf("clone golden projection %q: %w", name, err)
		}
		cloned.Golden.Projections[name] = clonedRows
	}
	return cloned, nil
}
