package trace_test

import (
	"testing"

	"github.com/weavegate/weavegate/internal/orchestrator"
	"github.com/weavegate/weavegate/internal/trace"
)

var (
	_ orchestrator.EventKind = trace.EventKind("")
	_ trace.EventKind        = orchestrator.EventKind("")

	_ orchestrator.ControlStatus = trace.ControlStatus("")
	_ trace.ControlStatus        = orchestrator.ControlStatus("")

	_ orchestrator.TerminalState = trace.TerminalState("")
	_ trace.TerminalState        = orchestrator.TerminalState("")

	_ orchestrator.WorkerFailureClass = trace.WorkerFailureClass("")
	_ trace.WorkerFailureClass        = orchestrator.WorkerFailureClass("")

	_ orchestrator.Event = trace.Event{}
	_ trace.Event        = orchestrator.Event{}

	_ orchestrator.WorkerTerminal = trace.WorkerTerminal{}
	_ trace.WorkerTerminal        = orchestrator.WorkerTerminal{}
)

func TestTraceModelOrchestratorAliases(t *testing.T) {
	var kind trace.EventKind = orchestrator.EventScheduleComplete
	var status trace.ControlStatus = orchestrator.ControlStatusNone
	var state trace.TerminalState = orchestrator.TerminalStateDone
	var failure trace.WorkerFailureClass = orchestrator.WorkerFailureNone

	if kind != trace.EventScheduleComplete || status != trace.ControlStatusNone ||
		state != trace.TerminalStateDone || failure != trace.WorkerFailureNone {
		t.Fatal("orchestrator aliases changed shared trace values")
	}
}
