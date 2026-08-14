package trace

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestTraceModelJSONRoundTripAndClone(t *testing.T) {
	value := struct {
		Events    Trace     `json:"events"`
		Terminals Terminals `json:"terminals"`
	}{
		Events: Trace{
			{Seq: 1, Kind: EventWorkerInvoked, Step: -1, Worker: "w1", Status: ControlStatusNone, FailureClass: WorkerFailureNone},
			{Seq: 2, Kind: EventPointArrived, Step: 0, Worker: "w1", Point: "after_read", Status: ControlStatusArrived, FailureClass: WorkerFailureNone},
			{Seq: 3, Kind: EventWorkerFailed, Step: 0, Worker: "w2", Status: ControlStatusNone, FailureClass: WorkerFailureMySQLDeadlock},
		},
		Terminals: Terminals{
			{Worker: "w1", State: TerminalStateDone, FailureClass: WorkerFailureNone},
			{Worker: "w2", State: TerminalStateFailed, FailureClass: WorkerFailureMySQLDeadlock},
		},
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal trace model: %v", err)
	}
	const wantJSON = `{"events":[{"seq":1,"kind":"worker_invoked","step":-1,"worker":"w1","point":"","status":"none","failure_class":"none"},{"seq":2,"kind":"point_arrived","step":0,"worker":"w1","point":"after_read","status":"arrived","failure_class":"none"},{"seq":3,"kind":"worker_failed","step":0,"worker":"w2","point":"","status":"none","failure_class":"mysql_deadlock_1213"}],"terminals":[{"worker":"w1","state":"done","failure_class":"none"},{"worker":"w2","state":"failed","failure_class":"mysql_deadlock_1213"}]}`
	if string(encoded) != wantJSON {
		t.Fatalf("trace JSON = %s, want %s", encoded, wantJSON)
	}

	var decoded struct {
		Events    Trace     `json:"events"`
		Terminals Terminals `json:"terminals"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal trace model: %v", err)
	}
	if !reflect.DeepEqual(decoded.Events, value.Events) ||
		!reflect.DeepEqual(decoded.Terminals, value.Terminals) {
		t.Fatalf("round trip changed trace model: got %#v", decoded)
	}

	clonedEvents := value.Events.Clone()
	clonedTerminals := value.Terminals.Clone()
	clonedEvents[0].Worker = "changed"
	clonedTerminals[0].Worker = "changed"
	if value.Events[0].Worker != "w1" || value.Terminals[0].Worker != "w1" {
		t.Fatalf("clone mutation changed source: events=%#v terminals=%#v", value.Events, value.Terminals)
	}

	// Alias compatibility is proved by the bidirectional compile-time assertions
	// in alias_test.go. go test compiles that external test package before this
	// marker can be emitted.
	t.Log("TRACE_MODEL_RESULT events=3 terminals=2 json=stable orchestrator_aliases=compatible")
}

func TestTraceModelCloneNormalizesEmptyCollections(t *testing.T) {
	tests := []struct {
		name      string
		trace     Trace
		terminals Terminals
	}{
		{name: "nil"},
		{name: "non-nil empty", trace: Trace{}, terminals: Terminals{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := struct {
				Trace     Trace     `json:"trace"`
				Terminals Terminals `json:"terminals"`
			}{
				Trace:     test.trace.Clone(),
				Terminals: test.terminals.Clone(),
			}

			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatalf("marshal empty trace model: %v", err)
			}
			const wantJSON = `{"trace":[],"terminals":[]}`
			if string(encoded) != wantJSON {
				t.Fatalf("empty trace JSON = %s, want %s", encoded, wantJSON)
			}
			if value.Trace == nil || value.Terminals == nil {
				t.Fatalf("clone retained nil collections: trace=%#v terminals=%#v", value.Trace, value.Terminals)
			}
		})
	}
}

func TestTraceModelCloneElementsStayScalarOnly(t *testing.T) {
	// Trace.Clone and Terminals.Clone intentionally copy only their slice backing
	// arrays. Adding a composite or reference field must fail this test until the
	// clone implementation is upgraded to deep-copy that field.
	assertScalarFields(t, reflect.TypeOf(Event{}))
	assertScalarFields(t, reflect.TypeOf(WorkerTerminal{}))
}

func assertScalarFields(t *testing.T, valueType reflect.Type) {
	t.Helper()
	for index := 0; index < valueType.NumField(); index++ {
		field := valueType.Field(index)
		switch field.Type.Kind() {
		case reflect.Bool,
			reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
			reflect.Uintptr, reflect.Float32, reflect.Float64,
			reflect.Complex64, reflect.Complex128, reflect.String:
			continue
		default:
			t.Errorf(
				"%s.%s has non-scalar kind %s; update Clone before adding this field",
				valueType.Name(),
				field.Name,
				field.Type.Kind(),
			)
		}
	}
}
