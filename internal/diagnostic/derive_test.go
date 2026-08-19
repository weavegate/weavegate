package diagnostic

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/weavegate/weavegate/internal/oracle"
	shippedrules "github.com/weavegate/weavegate/rules"
)

func TestDeriveContract(t *testing.T) {
	table, err := Load(shippedrules.FS())
	if err != nil {
		t.Fatal(err)
	}
	input := Input{
		Table: table,
		Violations: []oracle.Violation{
			{OracleID: "second", Kind: oracle.KindAssertion, Rows: []oracle.Row{{"z": int64(2), "a": int64(1)}}},
			{OracleID: "first", Kind: oracle.KindAssertion, Rows: []oracle.Row{{"count": int64(2)}}},
			{OracleID: "second", Kind: oracle.KindAssertion, Rows: []oracle.Row{{"ignored_for_observed": int64(3)}}},
		},
		OracleOrder: []string{"first", "second"}, TraceOracles: []string{"first", "second"}, Flaky: true,
		Fingerprints: map[string]int{"fp-a": 19, "fp-b": 1}, ScheduleRef: "sch_test",
	}
	got, err := Derive(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Assertion != "first" || got[1].Assertion != "second" || got[2].Code != "RG090" {
		t.Fatalf("diagnostics = %#v", got)
	}
	if got[1].Evidence.Rows != 1 || got[1].Evidence.EvidenceSets != 2 ||
		got[1].Observed != "second returned 1 row: a=1 z=2" {
		t.Fatalf("merged diagnostic = %#v", got[1])
	}
	if got[0].Evidence.EvidenceSets != 0 || got[0].Evidence.Trace != "trace.json" ||
		got[0].Evidence.Observation != "observation.json" {
		t.Fatalf("Oracle diagnostic evidence = %#v", got[0].Evidence)
	}
	if got[2].Assertion != "" || got[2].Evidence.Rows != 0 ||
		got[2].Evidence.Trace != "" || got[2].Evidence.Observation != "observation.json" {
		t.Fatalf("engine diagnostic = %#v", got[2])
	}
	if got[2].Observed != "repeated executions produced 2 normalized fingerprints" {
		t.Fatalf("flaky observed = %q", got[2].Observed)
	}
	want := got
	for range 20 {
		next, err := Derive(input)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(next, want) {
			t.Fatalf("unstable result:\nwant %#v\n got %#v", want, next)
		}
	}
	fmt.Println("DIAGNOSTIC_DERIVE_RESULT key=violation_kind config_input=none unit=code_and_oracle rows=representative_set evidence_sets=counted trace=corresponding_run_only observation=always order=oracle_declaration flaky=engine_last reserved_trigger=skipped unknown_kind=error implemented_kinds=all_mapped map_iteration=absent stable=true")
}

func TestDerivePointsOnlySupportedDiagnosticsAtTrace(t *testing.T) {
	table, err := Load(shippedrules.FS())
	if err != nil {
		t.Fatal(err)
	}
	got, err := Derive(Input{
		Table: table,
		Violations: []oracle.Violation{
			{OracleID: "first", Kind: oracle.KindAssertion, Rows: []oracle.Row{{"n": int64(1)}}},
			{OracleID: "second", Kind: oracle.KindAssertion, Rows: []oracle.Row{{"n": int64(2)}}},
		},
		OracleOrder:  []string{"first", "second"},
		TraceOracles: []string{"first"},
		ScheduleRef:  "sch_test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("diagnostics = %#v", got)
	}
	if got[0].Evidence.Trace != "trace.json" || got[0].Evidence.Observation != "observation.json" {
		t.Fatalf("first evidence = %#v", got[0].Evidence)
	}
	if got[1].Evidence.Trace != "" || got[1].Evidence.Observation != "observation.json" {
		t.Fatalf("second evidence = %#v", got[1].Evidence)
	}
}

func TestDeriveDescribesDiscoveryReplayMismatch(t *testing.T) {
	table, err := Load(shippedrules.FS())
	if err != nil {
		t.Fatal(err)
	}
	got, err := Derive(Input{
		Table: table, Flaky: true,
		Fingerprints:         map[string]int{"fp-replay": 20},
		DiscoveryFingerprint: "fp-discovery",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Code != "RG090" ||
		got[0].Observed != "the discovery fingerprint differs from the replay fingerprint" {
		t.Fatalf("diagnostics = %#v", got)
	}
}

func TestDerivePreservesFlakyVerdictWithoutDetailedDivergence(t *testing.T) {
	table, err := Load(shippedrules.FS())
	if err != nil {
		t.Fatal(err)
	}
	got, err := Derive(Input{
		Table: table, Flaky: true,
		Fingerprints:         map[string]int{"fp-same": 20},
		DiscoveryFingerprint: "fp-same",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Code != "RG090" ||
		got[0].Observed != "the determinism check reported divergent normalized results" {
		t.Fatalf("diagnostics = %#v", got)
	}
}

func TestDeriveSkipsKnownTriggerWithoutRule(t *testing.T) {
	got, err := Derive(Input{
		Table:       Table{byCode: map[Code]Rule{}, byTrigger: map[Trigger]Rule{}},
		Violations:  []oracle.Violation{{OracleID: "check", Kind: oracle.KindAssertion}},
		OracleOrder: []string{"check"},
	})
	if err != nil || len(got) != 0 {
		t.Fatalf("Derive = %#v, %v", got, err)
	}
}

func TestDeriveRejectsUnknownKind(t *testing.T) {
	_, err := Derive(Input{
		Violations:  []oracle.Violation{{OracleID: "check", Kind: oracle.Kind("future")}},
		OracleOrder: []string{"check"},
	})
	if err == nil {
		t.Fatal("Derive succeeded")
	}
}

func TestImplementedKindsHaveRules(t *testing.T) {
	table, err := Load(shippedrules.FS())
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []oracle.Kind{oracle.KindAssertion} {
		trigger, ok := TriggerForKind(kind)
		if !ok {
			t.Fatalf("kind %q has no trigger", kind)
		}
		if _, ok := table.LookupTrigger(trigger); !ok {
			t.Fatalf("kind %q trigger %q has no rule", kind, trigger)
		}
	}
}
