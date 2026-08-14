// Package trace defines the package-neutral, wall-clock-free evidence values
// shared by orchestration and post-run evaluation layers.
package trace

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

// WorkerFailureClass classifies terminal worker errors independently from
// timeout-inferred control observations.
type WorkerFailureClass string

const (
	WorkerFailureNone          WorkerFailureClass = "none"
	WorkerFailureError         WorkerFailureClass = "worker_error"
	WorkerFailureMySQLDeadlock WorkerFailureClass = "mysql_deadlock_1213"
)

// Event is one ordered, wall-clock-free trace observation. Step is the
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

// Trace preserves the realized event order for one run.
type Trace []Event

// Clone returns an independently mutable, non-nil copy while preserving event
// order. Empty evidence is normalized to [] when encoded as JSON.
func (t Trace) Clone() Trace {
	if t == nil {
		return Trace{}
	}
	return append(Trace{}, t...)
}

// WorkerTerminal is the normalized terminal data included in replay
// fingerprints. Raw error strings and worker durations are deliberately absent.
type WorkerTerminal struct {
	Worker       string             `json:"worker"`
	State        TerminalState      `json:"state"`
	FailureClass WorkerFailureClass `json:"failure_class"`
}

// Terminals preserves worker declaration order for one run.
type Terminals []WorkerTerminal

// Clone returns an independently mutable, non-nil copy while preserving worker
// order. Empty evidence is normalized to [] when encoded as JSON.
func (t Terminals) Clone() Terminals {
	if t == nil {
		return Terminals{}
	}
	return append(Terminals{}, t...)
}
