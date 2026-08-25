package oracle

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/weavegate/weavegate/internal/trace"
)

func TestSetPreservesPassResultsAndCanonicalizesFingerprint(t *testing.T) {
	returnedViolations := []Violation{
		{
			OracleID: "z-check",
			Kind:     KindAssertion,
			Rows: []Row{
				{"request_id": int64(42), "count": uint64(2)},
			},
		},
		{
			OracleID: "z-check",
			Kind:     KindAssertion,
			Rows: []Row{
				{"request_id": int64(7), "count": uint64(3)},
			},
		},
	}
	original := RunContext{
		Golden: &Snapshot{Projections: map[string][]Row{
			"requests": {{"request_id": int64(42)}},
		}},
		Trace: trace.Trace{
			{Seq: 1, Kind: trace.EventScheduleComplete, Step: -1, Status: trace.ControlStatusNone, FailureClass: trace.WorkerFailureNone},
		},
		Terminals: trace.Terminals{
			{Worker: "w1", State: trace.TerminalStateDone, FailureClass: trace.WorkerFailureNone},
		},
	}

	violating := &testOracle{id: "z-check"}
	violating.evaluate = func(_ context.Context, _ DB, run RunContext) ([]Violation, error) {
		run.Golden.Projections["requests"][0]["request_id"] = int64(99)
		run.Golden.Projections["added"] = []Row{}
		run.Trace[0].Worker = "changed"
		run.Terminals[0].Worker = "changed"
		return returnedViolations, nil
	}
	passing := &testOracle{id: "a-pass"}
	passing.evaluate = func(_ context.Context, _ DB, run RunContext) ([]Violation, error) {
		if got := run.Golden.Projections["requests"][0]["request_id"]; got != int64(42) {
			t.Fatalf("second oracle golden value = %v, want 42", got)
		}
		if _, exists := run.Golden.Projections["added"]; exists {
			t.Fatal("first oracle projection mutation leaked into second oracle")
		}
		if run.Trace[0].Worker != "" || run.Terminals[0].Worker != "w1" {
			t.Fatalf("first oracle evidence mutation leaked: trace=%#v terminals=%#v", run.Trace, run.Terminals)
		}
		return nil, nil
	}

	set, err := NewSet(violating, passing)
	if err != nil {
		t.Fatalf("create set: %v", err)
	}
	evaluation, err := set.Evaluate(context.Background(), nil, original)
	if err != nil {
		t.Fatalf("evaluate set: %v", err)
	}
	if len(evaluation.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(evaluation.Results))
	}
	if evaluation.Results[0].OracleID != "z-check" || evaluation.Results[1].OracleID != "a-pass" {
		t.Fatalf("result order = %#v, want declaration order z-check/a-pass", evaluation.Results)
	}
	if len(evaluation.Results[0].Violations) != 2 {
		t.Fatalf("z-check violations = %d, want 2", len(evaluation.Results[0].Violations))
	}
	if evaluation.Results[1].Violations == nil || len(evaluation.Results[1].Violations) != 0 {
		t.Fatalf("PASS result violations = %#v, want non-nil empty", evaluation.Results[1].Violations)
	}
	if got := evaluation.Results[0].Violations[0].Rows[0]["request_id"]; got != int64(42) {
		t.Fatalf("returned violation order changed: first request = %v, want 42", got)
	}
	if original.Trace[0].Worker != "" || original.Terminals[0].Worker != "w1" ||
		original.Golden.Projections["requests"][0]["request_id"] != int64(42) {
		t.Fatalf("oracle mutation changed original context: %#v", original)
	}

	returnedViolations[0].Rows[0]["request_id"] = int64(1000)
	returnedViolations[0].Rows = append(returnedViolations[0].Rows, Row{"request_id": int64(1001)})
	if got := evaluation.Results[0].Violations[0].Rows[0]["request_id"]; got != int64(42) {
		t.Fatalf("oracle post-return mutation changed evaluation: got %v", got)
	}

	rowSeven := Row{}
	rowSeven["count"] = uint64(3)
	rowSeven["request_id"] = int64(7)
	rowFortyTwo := Row{}
	rowFortyTwo["request_id"] = int64(42)
	rowFortyTwo["count"] = uint64(2)
	alternate, err := NewEvaluation(
		OracleResult{OracleID: "a-pass"},
		OracleResult{
			OracleID: "z-check",
			Violations: []Violation{
				{OracleID: "z-check", Kind: KindAssertion, Rows: []Row{rowSeven}},
				{OracleID: "z-check", Kind: KindAssertion, Rows: []Row{rowFortyTwo}},
			},
		},
	)
	if err != nil {
		t.Fatalf("create alternate evaluation: %v", err)
	}
	if alternate.Fingerprint != evaluation.Fingerprint {
		t.Fatalf("canonical order changed fingerprint: %q != %q", alternate.Fingerprint, evaluation.Fingerprint)
	}

	missingPass, err := NewEvaluation(evaluation.Results[0])
	if err != nil {
		t.Fatalf("create missing-pass evaluation: %v", err)
	}
	if missingPass.Fingerprint == evaluation.Fingerprint {
		t.Fatal("removing PASS oracle did not change fingerprint")
	}

	tampered := evaluation
	tampered.Fingerprint = "observed-output"
	if _, err := ValidateEvaluation(tampered); err == nil {
		t.Fatal("tampered evaluation validation error = nil")
	}

	t.Log(
		"ORACLE_SET_RESULT oracles=2 results=2 pass_results=1 violations=2 " +
			"declaration_order=preserved fingerprint_order=canonical " +
			"missing_oracle=changes_fingerprint invalid_result=error",
	)
}

func TestEvaluationNormalizesAndClonesCollections(t *testing.T) {
	inputRow := Row{"stable": "before"}
	input := []OracleResult{
		{OracleID: "pass-check"},
		{
			OracleID: "evidence-check",
			Violations: []Violation{
				{OracleID: "evidence-check", Kind: KindAssertion},
				{OracleID: "evidence-check", Kind: KindAssertion, Rows: []Row{inputRow}},
			},
		},
	}
	evaluation, err := NewEvaluation(input...)
	if err != nil {
		t.Fatalf("create evaluation: %v", err)
	}
	input[0].OracleID = "changed"
	input[1].Violations[1].Rows[0]["stable"] = "after"
	if evaluation.Results[0].OracleID != "pass-check" ||
		evaluation.Results[1].Violations[1].Rows[0]["stable"] != "before" {
		t.Fatalf("input mutation changed evaluation: %#v", evaluation)
	}
	if evaluation.Results == nil || evaluation.Results[0].Violations == nil ||
		evaluation.Results[1].Violations[0].Rows == nil {
		t.Fatalf("nil collection was not normalized: %#v", evaluation.Results)
	}

	encoded, err := json.Marshal(evaluation.Results)
	if err != nil {
		t.Fatalf("marshal normalized evaluation: %v", err)
	}
	if strings.Contains(string(encoded), "null") {
		t.Fatalf("normalized evaluation JSON contains null collection: %s", encoded)
	}

	validated, err := ValidateEvaluation(evaluation)
	if err != nil {
		t.Fatalf("validate evaluation: %v", err)
	}
	validated.Results[1].Violations[1].Rows[0]["stable"] = "validated-mutation"
	if evaluation.Results[1].Violations[1].Rows[0]["stable"] != "before" {
		t.Fatal("validated output aliases source evaluation")
	}
}

func TestEvaluationFingerprintCanonicalizesOnlyCopy(t *testing.T) {
	rows := []Row{
		{"key": "z", "count": int64(2)},
		{"key": "a", "count": int64(1)},
	}
	first, err := NewEvaluation(OracleResult{
		OracleID: "row-order",
		Violations: []Violation{
			{OracleID: "row-order", Kind: KindAssertion, Rows: rows},
		},
	})
	if err != nil {
		t.Fatalf("create first evaluation: %v", err)
	}
	second, err := NewEvaluation(OracleResult{
		OracleID: "row-order",
		Violations: []Violation{
			{OracleID: "row-order", Kind: KindAssertion, Rows: []Row{rows[1], rows[0]}},
		},
	})
	if err != nil {
		t.Fatalf("create second evaluation: %v", err)
	}
	if first.Fingerprint != second.Fingerprint {
		t.Fatalf("row order changed fingerprint: %q != %q", first.Fingerprint, second.Fingerprint)
	}
	if first.Results[0].Violations[0].Rows[0]["key"] != "z" {
		t.Fatalf("fingerprint sorting changed returned order: %#v", first.Results)
	}
	if len(first.Fingerprint) != 64 {
		t.Fatalf("fingerprint length = %d, want 64", len(first.Fingerprint))
	}
}

func TestEvaluationAcceptsSupportedEvidenceValues(t *testing.T) {
	evaluation, err := NewEvaluation(OracleResult{
		OracleID: "supported-values",
		Violations: []Violation{
			{
				OracleID: "supported-values",
				Kind:     KindAssertion,
				Rows: []Row{
					{
						"nil":     nil,
						"bool":    true,
						"int64":   int64(-42),
						"uint64":  uint64(42),
						"float64": float64(1.25),
						"문자열":     "유효한 UTF-8",
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("supported evidence values rejected: %v", err)
	}
	if _, err := ValidateEvaluation(evaluation); err != nil {
		t.Fatalf("supported evaluation validation: %v", err)
	}
}

func TestRejectsInvalidOracleSets(t *testing.T) {
	if _, err := NewSet(); err == nil {
		t.Fatal("empty set error = nil")
	}
	if _, err := NewSet(nil); err == nil {
		t.Fatal("nil oracle error = nil")
	}
	var typedNil *testOracle
	if _, err := NewSet(typedNil); err == nil {
		t.Fatal("typed nil oracle error = nil")
	}

	for _, id := range []string{"", "UPPER", " leading", "trailing ", "-leading", "has_underscore"} {
		t.Run("id_"+id, func(t *testing.T) {
			if _, err := NewSet(&testOracle{id: id}); err == nil {
				t.Fatalf("invalid ID %q error = nil", id)
			}
		})
	}
	for _, id := range []string{"trailing-", "two--parts"} {
		if _, err := NewSet(&testOracle{id: id}); err != nil {
			t.Fatalf("valid ID %q error = %v", id, err)
		}
	}
	if _, err := NewSet(&testOracle{id: "duplicate"}, &testOracle{id: "duplicate"}); err == nil {
		t.Fatal("duplicate ID error = nil")
	}

	mutable := &testOracle{id: "stable-id"}
	set, err := NewSet(mutable)
	if err != nil {
		t.Fatalf("create mutable set: %v", err)
	}
	mutable.id = "changed-id"
	if _, err := set.Evaluate(context.Background(), nil, RunContext{}); err == nil ||
		!strings.Contains(err.Error(), "stable ID changed") {
		t.Fatalf("changed stable ID error = %v", err)
	}
	var nilContext context.Context
	if _, err := set.Evaluate(nilContext, nil, RunContext{}); err == nil {
		t.Fatal("nil context error = nil")
	}
	var nilSet *Set
	if _, err := nilSet.Evaluate(context.Background(), nil, RunContext{}); err == nil {
		t.Fatal("nil set evaluation error = nil")
	}
}

func TestRejectsMalformedEvaluationAndEvidence(t *testing.T) {
	if _, err := NewEvaluation(); err == nil {
		t.Fatal("empty evaluation error = nil")
	}
	if _, err := ValidateEvaluation(Evaluation{}); err == nil {
		t.Fatal("empty evaluator result error = nil")
	}
	if _, err := ValidateEvaluation(Evaluation{Fingerprint: strings.Repeat("0", 64)}); err == nil ||
		!strings.Contains(err.Error(), "oracle result") {
		t.Fatalf("empty results protocol error = %v", err)
	}
	if _, err := NewEvaluation(
		OracleResult{OracleID: "same"},
		OracleResult{OracleID: "same"},
	); err == nil {
		t.Fatal("duplicate result ID error = nil")
	}

	invalidViolations := []struct {
		name      string
		violation Violation
	}{
		{name: "mismatched ID", violation: Violation{OracleID: "other", Kind: KindAssertion}},
		{name: "blank kind", violation: Violation{OracleID: "check"}},
		{name: "unknown kind", violation: Violation{OracleID: "check", Kind: Kind("future")}},
		{name: "nil row", violation: Violation{OracleID: "check", Kind: KindAssertion, Rows: []Row{nil}}},
	}
	for _, test := range invalidViolations {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewEvaluation(OracleResult{
				OracleID:   "check",
				Violations: []Violation{test.violation},
			}); err == nil {
				t.Fatal("malformed violation error = nil")
			}
		})
	}

	invalidUTF8 := string([]byte{0xff})
	invalidValues := []struct {
		name string
		row  Row
	}{
		{name: "blank key", row: Row{" ": int64(1)}},
		{name: "invalid key UTF-8", row: Row{invalidUTF8: int64(1)}},
		{name: "plain int", row: Row{"value": int(1)}},
		{name: "float32", row: Row{"value": float32(1)}},
		{name: "NaN", row: Row{"value": math.NaN()}},
		{name: "positive infinity", row: Row{"value": math.Inf(1)}},
		{name: "negative infinity", row: Row{"value": math.Inf(-1)}},
		{name: "invalid string UTF-8", row: Row{"value": invalidUTF8}},
		{name: "nested slice", row: Row{"value": []string{"nested"}}},
		{name: "nested map", row: Row{"value": map[string]any{"nested": true}}},
		{name: "pointer", row: Row{"value": new(int64)}},
	}
	for _, test := range invalidValues {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewEvaluation(OracleResult{
				OracleID: "check",
				Violations: []Violation{
					{OracleID: "check", Kind: KindAssertion, Rows: []Row{test.row}},
				},
			})
			if err == nil {
				t.Fatal("unsupported evidence error = nil")
			}
		})
	}

	valid, err := NewEvaluation(OracleResult{OracleID: "check"})
	if err != nil {
		t.Fatalf("create valid evaluation: %v", err)
	}
	valid.Fingerprint = ""
	if _, err := ValidateEvaluation(valid); err == nil {
		t.Fatal("empty fingerprint error = nil")
	}
	valid.Fingerprint = strings.Repeat("0", 64)
	if _, err := ValidateEvaluation(valid); err == nil {
		t.Fatal("mismatched fingerprint error = nil")
	}
}

func TestSetStopsAfterOracleEvaluationError(t *testing.T) {
	wantErr := errors.New("oracle unavailable")
	var calls []string
	first := &testOracle{id: "first-pass", evaluate: func(context.Context, DB, RunContext) ([]Violation, error) {
		calls = append(calls, "first-pass")
		return nil, nil
	}}
	second := &testOracle{id: "second-error", evaluate: func(context.Context, DB, RunContext) ([]Violation, error) {
		calls = append(calls, "second-error")
		return nil, wantErr
	}}
	third := &testOracle{id: "third-skipped", evaluate: func(context.Context, DB, RunContext) ([]Violation, error) {
		calls = append(calls, "third-skipped")
		return nil, nil
	}}
	set, err := NewSet(first, second, third)
	if err != nil {
		t.Fatalf("create set: %v", err)
	}
	evaluation, err := set.Evaluate(context.Background(), nil, RunContext{})
	if !errors.Is(err, wantErr) || !strings.Contains(err.Error(), `oracle "second-error"`) {
		t.Fatalf("evaluation error = %v, want wrapped second-error", err)
	}
	if !reflect.DeepEqual(calls, []string{"first-pass", "second-error"}) {
		t.Fatalf("oracle calls = %v, want first/second only", calls)
	}
	if !reflect.DeepEqual(evaluation, Evaluation{}) {
		t.Fatalf("partial evaluation returned on error: %#v", evaluation)
	}
}

func TestEvaluationEvaluatorFuncRejectsNil(t *testing.T) {
	var evaluator EvaluatorFunc
	if _, err := evaluator.Evaluate(context.Background(), nil, RunContext{}); err == nil {
		t.Fatal("nil EvaluatorFunc error = nil")
	}

	want, err := NewEvaluation(OracleResult{OracleID: "pass"})
	if err != nil {
		t.Fatalf("create expected evaluation: %v", err)
	}
	evaluator = func(context.Context, DB, RunContext) (Evaluation, error) {
		return want, nil
	}
	got, err := evaluator.Evaluate(context.Background(), nil, RunContext{})
	if err != nil {
		t.Fatalf("evaluate function: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("EvaluatorFunc result = %#v, want %#v", got, want)
	}
}

func TestCloneRunContextNormalizesAndRejectsInvalidGoldenRows(t *testing.T) {
	cloned, err := cloneRunContext(RunContext{})
	if err != nil {
		t.Fatalf("clone empty context: %v", err)
	}
	if cloned.Trace == nil || cloned.Terminals == nil {
		t.Fatalf("empty evidence was not normalized: %#v", cloned)
	}

	_, err = cloneRunContext(RunContext{Golden: &Snapshot{Projections: map[string][]Row{
		"invalid": {nil},
	}}})
	if err == nil || !strings.Contains(err.Error(), `projection "invalid"`) {
		t.Fatalf("invalid golden row error = %v", err)
	}
}

type testOracle struct {
	id       string
	evaluate func(context.Context, DB, RunContext) ([]Violation, error)
}

func (o *testOracle) ID() string {
	return o.id
}

func (o *testOracle) Evaluate(
	ctx context.Context,
	db DB,
	run RunContext,
) ([]Violation, error) {
	if o.evaluate == nil {
		return nil, nil
	}
	return o.evaluate(ctx, db, run)
}
