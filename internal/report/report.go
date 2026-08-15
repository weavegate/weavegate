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

// Scenario is the configured scenario together with the schedule this run
// reports on — the schedule discovered during exploration, or the schedule
// a replay was given.
type Scenario struct {
	Name              string             `json:"name"`
	Workers           []scenario.Worker  `json:"workers"`
	SyncPoints        []string           `json:"sync_points"`
	ViolatingSchedule *scenario.Schedule `json:"violating_schedule,omitempty"`
}

// Observation holds only the fields this run can actually compute (A-8).
// Fields belonging to unimplemented oracles are omitted rather than
// zero-filled, so "not measured" is never presented as "measured zero".
type Observation struct {
	SchedulesExplored   int      `json:"schedules_explored"`
	ExplorePasses       int      `json:"explore_passes"`
	AssertionViolations []string `json:"assertion_violations"`
	Repeat              int      `json:"repeat"`
	ViolationRuns       int      `json:"violation_runs"`
	Flaky               bool     `json:"flaky"`
}

// Trace is the run directory's trace.json shape.
type Trace struct {
	ScheduleRef string          `json:"schedule_ref"`
	Events      trace.Trace     `json:"events"`
	Terminals   trace.Terminals `json:"terminals"`
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
