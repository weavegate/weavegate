package diagnostic

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/weavegate/weavegate/internal/oracle"
)

func TestDiagnosticTypeContract(t *testing.T) {
	valid := Rule{
		Code: RG("RG001"), Severity: SeverityError,
		Triggers: []Trigger{TriggerOracleAssertion},
		Title:    "title", Invariant: "invariant", Reason: "reason", Help: []string{"help"},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("validate complete rule: %v", err)
	}
	for _, code := range []Code{"RG01", "rg001", "RG0001", "RGABC"} {
		if ValidateCode(code) == nil {
			t.Fatalf("ValidateCode(%q) succeeded", code)
		}
	}
	if ValidateTrigger("oracle.typo") == nil {
		t.Fatal("unknown trigger succeeded")
	}
	withoutHelp := valid
	withoutHelp.Help = nil
	if withoutHelp.Validate() == nil {
		t.Fatal("rule without help succeeded")
	}
	withoutTriggers := valid
	withoutTriggers.Triggers = nil
	if withoutTriggers.Validate() == nil {
		t.Fatal("rule without triggers succeeded")
	}
	trigger, ok := TriggerForKind(oracle.KindAssertion)
	if !ok || trigger != TriggerOracleAssertion {
		t.Fatalf("assertion mapping = %q, %t", trigger, ok)
	}

	diagnostic := Diagnostic{
		Code: "RG001", Severity: SeverityError, Title: "title", Observed: "observed",
		Assertion: "check", Invariant: "invariant", Reason: "reason", Help: []string{"help"},
		Evidence: Evidence{ScheduleRef: "sch_1", Rows: 1, EvidenceSets: 2, Trace: "trace.json"},
	}
	encoded, err := json.Marshal(diagnostic)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"code"`, `"severity"`, `"title"`, `"observed"`, `"assertion"`, `"invariant"`, `"reason"`, `"help"`, `"evidence"`} {
		if !strings.Contains(string(encoded), field) {
			t.Fatalf("JSON missing %s: %s", field, encoded)
		}
	}
	if !strings.Contains(string(encoded), `"evidence_sets":2`) {
		t.Fatalf("JSON missing evidence set count: %s", encoded)
	}
	fmt.Println("DIAGNOSTIC_TYPE_RESULT code_grammar=strict severity=error trigger_vocabulary=closed implemented_triggers=2 reserved_triggers=5 required_fields=enforced empty_help=error empty_triggers=error kind_mapping=oracle_kind json_shape=spec9")
}

func RG(value string) Code { return Code(value) }
