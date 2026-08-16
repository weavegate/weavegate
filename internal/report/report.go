// Package report writes the six run directory artifacts a weavegate run
// leaves behind, and renders the human-readable summary. Two files are
// volatile per run (manifest.json, report.json); the remaining four are
// deterministic — the same inputs produce byte-identical output. See
// docs/adr/0005-volatile-run-metadata-boundary.md.
package report

import (
	"time"

	"github.com/weavegate/weavegate/internal/scenario"
	"github.com/weavegate/weavegate/internal/trace"
)

// Manifest is volatile per-run metadata: identifiers, timestamps, and
// environment facts collected immediately after fixture provisioning
// (A-7). This package never queries the database itself.
type Manifest struct {
	RunID            string    `json:"run_id"`
	StartedAt        time.Time `json:"started_at"`
	WeavegateVersion string    `json:"weavegate_version"`
	SchemaVersion    string    `json:"schema_version"`
	SeedData         string    `json:"seed_data"`
	IsolationLevel   string    `json:"isolation_level"`
	Engine           string    `json:"engine"`
	Adapter          string    `json:"adapter"`
	Variant          string    `json:"variant"`
	Image            string    `json:"image"`
}

// Worker binds a stable worker ID to an adapter command. It is this
// package's own serialization shape for scenario.Worker: the engine type
// carries no JSON tags of its own, so encoding it directly would leak
// exported Go field names (ID/Command) instead of this package's
// lowercase snake_case contract.
type Worker struct {
	ID      string `json:"id"`
	Command string `json:"command"`
}

// NewWorkers converts scenario workers into this package's own JSON shape.
func NewWorkers(workers []scenario.Worker) []Worker {
	converted := make([]Worker, 0, len(workers))
	for _, worker := range workers {
		converted = append(converted, Worker{ID: worker.ID, Command: worker.Command})
	}
	return converted
}

// CoordinationStep identifies one worker arrival that a replay intends to
// release, mirroring scenario.CoordinationStep as this package's own
// serialization shape.
type CoordinationStep struct {
	Worker string `json:"worker"`
	Point  string `json:"point"`
}

// Schedule is a content-addressed total order of coordination intents,
// mirroring scenario.Schedule as this package's own serialization shape.
type Schedule struct {
	ID    string             `json:"id"`
	Steps []CoordinationStep `json:"steps"`
}

// NewSchedule converts a scenario schedule into this package's own JSON
// shape, or returns nil when schedule is nil (a passing run with nothing to
// report).
func NewSchedule(schedule *scenario.Schedule) *Schedule {
	if schedule == nil {
		return nil
	}
	steps := make([]CoordinationStep, 0, len(schedule.Steps))
	for _, step := range schedule.Steps {
		steps = append(steps, CoordinationStep{Worker: step.Worker, Point: step.Point})
	}
	return &Schedule{ID: schedule.ID, Steps: steps}
}

// Scenario is the configured scenario together with the schedule this run
// reports on — the schedule discovered during exploration, or the schedule
// a replay was given. Params is the effective worker Args shared by every
// worker in the scenario (config validation requires them to be identical),
// so the evidence records which parameters produced this verdict even if
// the referenced config is later edited or deleted.
type Scenario struct {
	Name              string            `json:"name"`
	Workers           []Worker          `json:"workers"`
	SyncPoints        []string          `json:"sync_points"`
	Params            map[string]string `json:"params"`
	ViolatingSchedule *Schedule         `json:"violating_schedule,omitempty"`
}

// OracleDeclaration is one assertion's effective declaration (config's
// oracle.assertions entry), persisted alongside the run's evidence so a
// saved verdict can still be audited against the query that produced it
// even if the referenced config is later edited or deleted.
type OracleDeclaration struct {
	ID         string `json:"id"`
	SQL        string `json:"sql"`
	ExpectRows int    `json:"expect_rows"`
}

// Row is deterministic, JSON-safe evidence keyed by a stable column name.
// It mirrors oracle.Row's shape as this package's own type, rather than
// exposing the oracle package's type in the report contract.
type Row map[string]any

// AssertionViolation is one Oracle's violation evidence: the rows that
// caused an assertion to fail, mirroring oracle.Violation.Rows. Distinct
// violations of the same OracleID (for example, the discovery run and a
// replay run reproducing it with different rows) are kept as separate
// entries rather than merged, so each is individually auditable.
type AssertionViolation struct {
	OracleID string `json:"oracle_id"`
	Rows     []Row  `json:"rows"`
}

// Observation holds only the fields this run can actually compute (A-8).
// Fields belonging to unimplemented oracles are omitted rather than
// zero-filled, so "not measured" is never presented as "measured zero".
type Observation struct {
	SchedulesExplored   int                  `json:"schedules_explored"`
	ExplorePasses       int                  `json:"explore_passes"`
	AssertionViolations []AssertionViolation `json:"assertion_violations"`
	Oracles             []OracleDeclaration  `json:"oracles"`
	Repeat              int                  `json:"repeat"`
	ViolationRuns       int                  `json:"violation_runs"`
	Flaky               bool                 `json:"flaky"`

	// Fingerprints counts, per distinct normalized execution fingerprint,
	// how many of the repeat replay runs produced it. A run can be flaky
	// purely from this divergence (differing terminal states or timing
	// classification) with zero assertion violations anywhere, so this is
	// the only evidence of *why* such a run is flaky.
	Fingerprints map[string]int `json:"fingerprints"`
}

// Event is one ordered, wall-clock-free trace observation, mirroring
// trace.Event as this package's own serialization shape.
type Event struct {
	Seq          int    `json:"seq"`
	Kind         string `json:"kind"`
	Step         int    `json:"step"`
	Worker       string `json:"worker"`
	Point        string `json:"point"`
	Status       string `json:"status"`
	FailureClass string `json:"failure_class"`
}

// WorkerTerminal is the normalized terminal data included in replay
// fingerprints, mirroring trace.WorkerTerminal as this package's own
// serialization shape.
type WorkerTerminal struct {
	Worker       string `json:"worker"`
	State        string `json:"state"`
	FailureClass string `json:"failure_class"`
}

// Trace is the run directory's trace.json shape.
type Trace struct {
	ScheduleRef string           `json:"schedule_ref"`
	Events      []Event          `json:"events"`
	Terminals   []WorkerTerminal `json:"terminals"`
}

// NewTrace converts an engine trace into this package's own JSON shape.
func NewTrace(scheduleRef string, events trace.Trace, terminals trace.Terminals) Trace {
	convertedEvents := make([]Event, 0, len(events))
	for _, event := range events {
		convertedEvents = append(convertedEvents, Event{
			Seq:          event.Seq,
			Kind:         string(event.Kind),
			Step:         event.Step,
			Worker:       event.Worker,
			Point:        event.Point,
			Status:       string(event.Status),
			FailureClass: string(event.FailureClass),
		})
	}
	convertedTerminals := make([]WorkerTerminal, 0, len(terminals))
	for _, terminal := range terminals {
		convertedTerminals = append(convertedTerminals, WorkerTerminal{
			Worker:       terminal.Worker,
			State:        string(terminal.State),
			FailureClass: string(terminal.FailureClass),
		})
	}
	return Trace{ScheduleRef: scheduleRef, Events: convertedEvents, Terminals: convertedTerminals}
}

// Merged is the report.json shape: manifest, scenario, and observation in
// one file. It inherits manifest's volatile fields, so it is not part of
// the deterministic file set.
type Merged struct {
	Manifest    Manifest    `json:"manifest"`
	Scenario    Scenario    `json:"scenario"`
	Observation Observation `json:"observation"`
}

// Run is everything needed to write one run directory.
type Run struct {
	Manifest    Manifest
	Scenario    Scenario
	Observation Observation
	Trace       Trace

	// Pass is true when the run found no violation. Flaky takes priority
	// over Pass in the rendered headline (A-17): a run can be neither a
	// clean pass nor a stable violation.
	Pass  bool
	Flaky bool

	// ReplayCommand is the literal command line a reader can copy, paste,
	// and run to reproduce this run's schedule (A-5). Empty when the run
	// has no schedule to replay (an exhausted PASS with no discovery).
	ReplayCommand string
}
