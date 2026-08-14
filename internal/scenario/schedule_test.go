package scenario

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/weavegate/weavegate/internal/sut"
)

const matchingScheduleID = "sch_ba00582f9632"

func TestSavedScheduleReplay(t *testing.T) {
	scenario := matchingScenario()
	schedule, err := LoadScheduleFile("../../fixtures/matching-slice/schedules/concurrent-assign.json")
	if err != nil {
		t.Fatalf("load matching schedule: %v", err)
	}
	if err := Validate(scenario, schedule); err != nil {
		t.Fatalf("validate matching schedule: %v", err)
	}
	if schedule.ID != matchingScheduleID {
		t.Fatalf("schedule ID = %q, want %q", schedule.ID, matchingScheduleID)
	}
	if len(schedule.Steps) != 4 {
		t.Fatalf("schedule steps = %d, want 4", len(schedule.Steps))
	}

	encoded, err := json.Marshal(schedule)
	if err != nil {
		t.Fatalf("marshal matching schedule: %v", err)
	}
	roundTripped, err := LoadSchedule(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("load round-tripped matching schedule: %v", err)
	}
	if !reflect.DeepEqual(roundTripped, schedule) {
		t.Fatalf("round-tripped schedule = %#v, want %#v", roundTripped, schedule)
	}

	tampered := bytes.Replace(
		encoded,
		[]byte("before_insert_assignment"),
		[]byte("after_insert_assignment"),
		1,
	)
	if _, err := LoadSchedule(bytes.NewReader(tampered)); err == nil || !strings.Contains(err.Error(), "content ID mismatch") {
		t.Fatalf("load tampered schedule error = %v, want content ID mismatch", err)
	}

	t.Log(
		"SCHEDULE_REPLAY_RESULT id=sch_ba00582f9632 steps=4 workers=2 " +
			"per_worker_order=valid roundtrip=stable tamper=error",
	)
}

func TestContentIDUsesOnlyCanonicalSteps(t *testing.T) {
	steps := matchingSteps()
	id, err := ContentID(steps)
	if err != nil {
		t.Fatalf("derive content ID: %v", err)
	}
	if id != matchingScheduleID {
		t.Fatalf("content ID = %q, want %q", id, matchingScheduleID)
	}

	schedule, err := NewSchedule(steps)
	if err != nil {
		t.Fatalf("new schedule: %v", err)
	}
	steps[0].Point = "caller_mutation"
	if schedule.Steps[0].Point != "after_read_request" {
		t.Fatalf("new schedule retained caller-owned steps: %#v", schedule.Steps)
	}

	clone := schedule.Clone()
	clone.Steps[0].Point = "clone_mutation"
	if schedule.Steps[0].Point != "after_read_request" {
		t.Fatalf("schedule clone shares steps: %#v", schedule.Steps)
	}
}

func TestScenarioCloneCopiesMutableValues(t *testing.T) {
	original := matchingScenario()
	clone := original.Clone()

	clone.Workers[0].ID = "changed"
	clone.SyncPoints[0] = "changed"
	clone.SUTConfig.Params["request_id"] = "changed"

	if original.Workers[0].ID != "w1" {
		t.Fatalf("scenario clone shares workers: %#v", original.Workers)
	}
	if original.SyncPoints[0] != "after_read_request" {
		t.Fatalf("scenario clone shares sync-points: %#v", original.SyncPoints)
	}
	if original.SUTConfig.Params["request_id"] != "42" {
		t.Fatalf("scenario clone shares SUT params: %#v", original.SUTConfig.Params)
	}
}

func TestValidateRejectsInvalidScenario(t *testing.T) {
	validSchedule := mustSchedule(t, matchingSteps())

	tests := []struct {
		name     string
		mutate   func(*Scenario)
		wantPart string
	}{
		{
			name:     "empty name",
			mutate:   func(value *Scenario) { value.Name = " " },
			wantPart: "name is required",
		},
		{
			name:     "no workers",
			mutate:   func(value *Scenario) { value.Workers = nil },
			wantPart: "at least one worker",
		},
		{
			name:     "empty worker",
			mutate:   func(value *Scenario) { value.Workers[0].ID = "" },
			wantPart: "worker[0]",
		},
		{
			name:     "empty command",
			mutate:   func(value *Scenario) { value.Workers[0].Command = "" },
			wantPart: "command is required",
		},
		{
			name: "duplicate worker",
			mutate: func(value *Scenario) {
				value.Workers[1].ID = value.Workers[0].ID
			},
			wantPart: "duplicate worker",
		},
		{
			name:     "no points",
			mutate:   func(value *Scenario) { value.SyncPoints = nil },
			wantPart: "at least one sync-point",
		},
		{
			name:     "empty point",
			mutate:   func(value *Scenario) { value.SyncPoints[0] = "" },
			wantPart: "sync-point[0]",
		},
		{
			name: "duplicate point",
			mutate: func(value *Scenario) {
				value.SyncPoints[1] = value.SyncPoints[0]
			},
			wantPart: "duplicate point",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scenario := matchingScenario()
			test.mutate(&scenario)

			err := Validate(scenario, validSchedule)
			if err == nil || !strings.Contains(err.Error(), test.wantPart) {
				t.Fatalf("validation error = %v, want containing %q", err, test.wantPart)
			}
		})
	}
}

func TestValidateRejectsInvalidSchedule(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*Schedule)
		rehash   bool
		wantPart string
	}{
		{
			name:     "empty ID",
			mutate:   func(value *Schedule) { value.ID = "" },
			wantPart: "ID is required",
		},
		{
			name: "stale ID",
			mutate: func(value *Schedule) {
				value.Steps[0].Point = "changed"
			},
			wantPart: "content ID mismatch",
		},
		{
			name: "empty worker",
			mutate: func(value *Schedule) {
				value.Steps[0].Worker = ""
			},
			rehash:   true,
			wantPart: "worker is required",
		},
		{
			name: "unknown worker",
			mutate: func(value *Schedule) {
				value.Steps[0].Worker = "w3"
			},
			rehash:   true,
			wantPart: "unknown worker",
		},
		{
			name: "empty point",
			mutate: func(value *Schedule) {
				value.Steps[0].Point = ""
			},
			rehash:   true,
			wantPart: "point is required",
		},
		{
			name: "unknown point",
			mutate: func(value *Schedule) {
				value.Steps[0].Point = "unknown"
			},
			rehash:   true,
			wantPart: "unknown point",
		},
		{
			name: "missing pair",
			mutate: func(value *Schedule) {
				value.Steps = value.Steps[:len(value.Steps)-1]
			},
			rehash:   true,
			wantPart: "missing pair",
		},
		{
			name: "duplicate pair",
			mutate: func(value *Schedule) {
				value.Steps[1] = value.Steps[0]
			},
			rehash:   true,
			wantPart: "duplicate pair",
		},
		{
			name: "per-worker reverse order",
			mutate: func(value *Schedule) {
				value.Steps[0], value.Steps[2] = value.Steps[2], value.Steps[0]
			},
			rehash:   true,
			wantPart: "out of order",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			schedule := mustSchedule(t, matchingSteps())
			test.mutate(&schedule)
			if test.rehash {
				var err error
				schedule.ID, err = ContentID(schedule.Steps)
				if err != nil {
					t.Fatalf("rehash schedule: %v", err)
				}
			}

			err := Validate(matchingScenario(), schedule)
			if err == nil || !strings.Contains(err.Error(), test.wantPart) {
				t.Fatalf("validation error = %v, want containing %q", err, test.wantPart)
			}
			for _, part := range []string{"scenario", matchingScenario().Name} {
				if !strings.Contains(err.Error(), part) {
					t.Errorf("validation error %q lacks context %q", err, part)
				}
			}
		})
	}
}

func TestValidateAllowsGlobalInterleaving(t *testing.T) {
	steps := []CoordinationStep{
		{Worker: "w1", Point: "after_read_request"},
		{Worker: "w1", Point: "before_insert_assignment"},
		{Worker: "w2", Point: "after_read_request"},
		{Worker: "w2", Point: "before_insert_assignment"},
	}

	if err := Validate(matchingScenario(), mustSchedule(t, steps)); err != nil {
		t.Fatalf("validate alternate global interleaving: %v", err)
	}
}

func TestLoadScheduleRejectsMalformedJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantPart string
	}{
		{
			name:     "unknown schedule field",
			input:    `{"id":"sch_deadbeef0000","steps":[],"strategy":"all"}`,
			wantPart: "unknown field",
		},
		{
			name: "unknown step field",
			input: `{"id":"sch_deadbeef0000","steps":[` +
				`{"worker":"w1","point":"p1","timeout":"1s"}]}`,
			wantPart: "unknown field",
		},
		{
			name: "trailing JSON",
			input: `{"id":"sch_4f53cda18c2b","steps":[]}` +
				` {"id":"sch_4f53cda18c2b","steps":[]}`,
			wantPart: "trailing JSON value",
		},
		{
			name:     "stale ID",
			input:    `{"id":"sch_deadbeef0000","steps":[]}`,
			wantPart: "content ID mismatch",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := LoadSchedule(strings.NewReader(test.input))
			if err == nil || !strings.Contains(err.Error(), test.wantPart) {
				t.Fatalf("load error = %v, want containing %q", err, test.wantPart)
			}
		})
	}

	if _, err := LoadSchedule(nil); err == nil || !strings.Contains(err.Error(), "reader is required") {
		t.Fatalf("load nil reader error = %v, want reader context", err)
	}
}

func matchingScenario() Scenario {
	return Scenario{
		Name: "matching-concurrent-assign",
		Workers: []Worker{
			{ID: "w1", Command: "assign"},
			{ID: "w2", Command: "assign"},
		},
		SyncPoints: []string{"after_read_request", "before_insert_assignment"},
		SUTConfig: sut.SUTConfig{
			Variant: "vulnerable",
			Params:  map[string]string{"request_id": "42"},
		},
	}
}

func matchingSteps() []CoordinationStep {
	return []CoordinationStep{
		{Worker: "w1", Point: "after_read_request"},
		{Worker: "w2", Point: "after_read_request"},
		{Worker: "w1", Point: "before_insert_assignment"},
		{Worker: "w2", Point: "before_insert_assignment"},
	}
}

func mustSchedule(t *testing.T, steps []CoordinationStep) Schedule {
	t.Helper()

	schedule, err := NewSchedule(steps)
	if err != nil {
		t.Fatalf("new schedule: %v", err)
	}
	return schedule
}
