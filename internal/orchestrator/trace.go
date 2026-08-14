package orchestrator

import (
	"fmt"

	"github.com/weavegate/weavegate/internal/trace"
)

// EventKind identifies one deterministic orchestration observation.
type EventKind = trace.EventKind

// All previously exported orchestrator event constants remain aliases of the
// shared model during migration.
const (
	EventFixtureReset        = trace.EventFixtureReset
	EventWorkerRegistered    = trace.EventWorkerRegistered
	EventWorkerInvoked       = trace.EventWorkerInvoked
	EventPointArrived        = trace.EventPointArrived
	EventPointTimeout        = trace.EventPointTimeout
	EventPointReleased       = trace.EventPointReleased
	EventStepTerminalSkipped = trace.EventStepTerminalSkipped
	EventWorkerDone          = trace.EventWorkerDone
	EventWorkerFailed        = trace.EventWorkerFailed
	EventScheduleComplete    = trace.EventScheduleComplete
)

// ControlStatus describes nonterminal control-plane observations. It is kept
// separate from worker terminal failure classification.
type ControlStatus = trace.ControlStatus

// All previously exported orchestrator control-status constants remain aliases
// of the shared model during migration.
const (
	ControlStatusNone            = trace.ControlStatusNone
	ControlStatusArrived         = trace.ControlStatusArrived
	ControlStatusTimeoutInferred = trace.ControlStatusTimeoutInferred
	ControlStatusReleased        = trace.ControlStatusReleased
	ControlStatusTerminalSkipped = trace.ControlStatusTerminalSkipped
)

// TerminalState is the normalized outcome of a worker invocation.
type TerminalState = trace.TerminalState

// All previously exported orchestrator terminal-state constants remain aliases
// of the shared model during migration.
const (
	TerminalStateDone   = trace.TerminalStateDone
	TerminalStateFailed = trace.TerminalStateFailed
)

// Event is kept as an alias while callers migrate to the shared trace model.
type Event = trace.Event

// WorkerTerminal is kept as an alias while callers migrate to the shared trace
// model.
type WorkerTerminal = trace.WorkerTerminal

// EventObserver synchronously receives a value copy of each appended event.
type EventObserver func(Event) error

type traceRecorder struct {
	events   trace.Trace
	observer EventObserver
}

func newTraceRecorder(observer EventObserver) *traceRecorder {
	return &traceRecorder{observer: observer}
}

func (r *traceRecorder) emit(event Event) error {
	event.Seq = len(r.events) + 1
	if event.Status == "" {
		event.Status = ControlStatusNone
	}
	if event.FailureClass == "" {
		event.FailureClass = WorkerFailureNone
	}
	r.events = append(r.events, event)
	if r.observer != nil {
		if err := r.observer(event); err != nil {
			return fmt.Errorf("observe trace event %d %q: %w", event.Seq, event.Kind, err)
		}
	}
	return nil
}

func (r *traceRecorder) clone() trace.Trace {
	return r.events.Clone()
}
