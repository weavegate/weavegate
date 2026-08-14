package orchestrator

import "fmt"

// EventKind identifies one deterministic orchestration observation.
type EventKind string

const (
	EventFixtureReset        EventKind = "fixture_reset"
	EventWorkerRegistered    EventKind = "worker_registered"
	EventWorkerInvoked       EventKind = "worker_invoked"
	EventPointArrived        EventKind = "point_arrived"
	EventPointTimeout        EventKind = "point_timeout"
	EventPointReleased       EventKind = "point_released"
	EventStepTerminalSkipped EventKind = "step_terminal_skipped"
	EventWorkerDone          EventKind = "worker_done"
	EventWorkerFailed        EventKind = "worker_failed"
	EventScheduleComplete    EventKind = "schedule_complete"
)

// ControlStatus describes nonterminal control-plane observations. It is kept
// separate from worker terminal failure classification.
type ControlStatus string

const (
	ControlStatusNone            ControlStatus = "none"
	ControlStatusArrived         ControlStatus = "arrived"
	ControlStatusTimeoutInferred ControlStatus = "timeout_inferred"
	ControlStatusReleased        ControlStatus = "released"
	ControlStatusTerminalSkipped ControlStatus = "terminal_skipped"
)

// TerminalState is the normalized outcome of a worker invocation.
type TerminalState string

const (
	TerminalStateDone   TerminalState = "done"
	TerminalStateFailed TerminalState = "failed"
)

// Event is the ordered, wall-clock-free trace draft for one run. Step is the
// zero-based saved schedule index, or -1 for lifecycle events without a step.
type Event struct {
	Seq          int                `json:"seq"`
	Kind         EventKind          `json:"kind"`
	Step         int                `json:"step"`
	Worker       string             `json:"worker"`
	Point        string             `json:"point"`
	Status       ControlStatus      `json:"status"`
	FailureClass WorkerFailureClass `json:"failure_class"`
}

// WorkerTerminal is the normalized terminal data included in replay
// fingerprints. Raw error strings and worker durations are deliberately absent.
type WorkerTerminal struct {
	Worker       string             `json:"worker"`
	State        TerminalState      `json:"state"`
	FailureClass WorkerFailureClass `json:"failure_class"`
}

// EventObserver synchronously receives a value copy of each appended event.
type EventObserver func(Event) error

type traceRecorder struct {
	events   []Event
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

func (r *traceRecorder) clone() []Event {
	return append([]Event(nil), r.events...)
}
