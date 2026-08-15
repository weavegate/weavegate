package scenario

import (
	"iter"
	"reflect"
	"strings"
	"testing"
)

func TestExhaustiveSchedulesCanonicalOrder(t *testing.T) {
	value := matchingScenario()
	plan, err := (Exhaustive{}).Schedules(value)
	if err != nil {
		t.Fatalf("build exhaustive schedule plan: %v", err)
	}
	if !plan.TotalKnown {
		t.Fatal("exhaustive schedule total is unknown, want known")
	}
	if plan.Total != 6 {
		t.Fatalf("exhaustive schedule total = %d, want 6", plan.Total)
	}
	if plan.Seq == nil {
		t.Fatal("exhaustive schedule sequence is nil")
	}

	schedules := collectSchedules(plan.Seq)
	if len(schedules) != plan.Total {
		t.Fatalf("yielded schedules = %d, want %d", len(schedules), plan.Total)
	}

	wantWorkerOrders := []string{
		"w1 w1 w2 w2",
		"w1 w2 w1 w2",
		"w1 w2 w2 w1",
		"w2 w1 w1 w2",
		"w2 w1 w2 w1",
		"w2 w2 w1 w1",
	}
	seenIDs := make(map[string]struct{}, len(schedules))
	for index, schedule := range schedules {
		if err := Validate(value, schedule); err != nil {
			t.Errorf("validate candidate %d: %v", index+1, err)
		}

		workerOrder := make([]string, 0, len(schedule.Steps))
		for _, step := range schedule.Steps {
			workerOrder = append(workerOrder, step.Worker)
		}
		if got := strings.Join(workerOrder, " "); got != wantWorkerOrders[index] {
			t.Errorf("candidate %d worker order = %q, want %q", index+1, got, wantWorkerOrders[index])
		}

		if _, exists := seenIDs[schedule.ID]; exists {
			t.Errorf("candidate %d repeats schedule ID %q", index+1, schedule.ID)
		}
		seenIDs[schedule.ID] = struct{}{}
	}

	saved, err := LoadScheduleFile("../../fixtures/matching-slice/schedules/concurrent-assign.json")
	if err != nil {
		t.Fatalf("load committed matching schedule: %v", err)
	}
	if !reflect.DeepEqual(schedules[1], saved) {
		t.Fatalf("candidate 2 = %#v, want committed schedule %#v", schedules[1], saved)
	}

	if _, err := (Exhaustive{MaxCandidates: 5}).Schedules(value); err == nil || !strings.Contains(err.Error(), "more than 5") {
		t.Fatalf("cap-exceeded error = %v, want more than 5", err)
	}
	invalid := value.Clone()
	invalid.Name = " "
	if _, err := (Exhaustive{}).Schedules(invalid); err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("invalid-scenario error = %v, want name is required", err)
	}

	t.Log(
		"EXHAUSTIVE_STRATEGY_RESULT workers=2 points=2 candidates=6 total_known=true " +
			"order=canonical per_worker_order=preserved unique_ids=6 saved_schedule_index=2 " +
			"cap_exceeded=error invalid_scenario=error",
	)
}

func TestCandidateCount(t *testing.T) {
	tests := []struct {
		name        string
		value       Scenario
		max         int
		want        int
		wantErrPart string
	}{
		{
			name:  "two workers and two points",
			value: matchingScenario(),
			max:   6,
			want:  6,
		},
		{
			name:  "one worker",
			value: oneWorkerScenario(),
			max:   1,
			want:  1,
		},
		{
			name:  "three workers and two points",
			value: threeWorkerScenario(),
			max:   90,
			want:  90,
		},
		{
			name:        "over bound",
			value:       threeWorkerScenario(),
			max:         89,
			wantErrPart: "more than 89",
		},
		{
			name:        "non-positive bound",
			value:       matchingScenario(),
			max:         0,
			wantErrPart: "max must be positive",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := CandidateCount(test.value, test.max)
			if test.wantErrPart != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErrPart) {
					t.Fatalf("candidate count error = %v, want containing %q", err, test.wantErrPart)
				}
				return
			}
			if err != nil {
				t.Fatalf("count candidates: %v", err)
			}
			if got != test.want {
				t.Fatalf("candidate count = %d, want %d", got, test.want)
			}
		})
	}
}

func TestExhaustiveCandidateLimitConfiguration(t *testing.T) {
	large := matchingScenario()
	large.Workers = append(large.Workers, Worker{ID: "w3", Command: "assign"})
	large.SyncPoints = []string{"p1", "p2", "p3", "p4"}

	if _, err := (Exhaustive{}).Schedules(large); err == nil || !strings.Contains(err.Error(), "more than 5000") {
		t.Fatalf("default candidate limit error = %v, want more than 5000", err)
	}

	plan, err := (Exhaustive{MaxCandidates: 40000}).Schedules(large)
	if err != nil {
		t.Fatalf("build plan with raised candidate limit: %v", err)
	}
	if plan.Total != 34650 {
		t.Fatalf("raised-limit candidate total = %d, want 34650", plan.Total)
	}

	if _, err := (Exhaustive{MaxCandidates: -1}).Schedules(matchingScenario()); err == nil || !strings.Contains(err.Error(), "must not be negative") {
		t.Fatalf("negative candidate limit error = %v, want negative limit rejection", err)
	}
}

func TestStrategySequenceIsRepeatableAndSupportsEarlyStop(t *testing.T) {
	value := matchingScenario()
	plan, err := (Exhaustive{}).Schedules(value)
	if err != nil {
		t.Fatalf("build exhaustive schedule plan: %v", err)
	}

	first := collectSchedules(plan.Seq)
	second := collectSchedules(plan.Seq)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated sequence differs:\nfirst:  %#v\nsecond: %#v", first, second)
	}

	yielded := 0
	for range plan.Seq {
		yielded++
		if yielded == 2 {
			break
		}
	}
	if yielded != 2 {
		t.Fatalf("early-stopped sequence yielded %d candidates, want 2", yielded)
	}

	value.Workers[0].ID = "mutated"
	afterMutation := collectSchedules(plan.Seq)
	if !reflect.DeepEqual(first, afterMutation) {
		t.Fatal("schedule plan retained caller-owned scenario slices")
	}
}

func collectSchedules(sequence iter.Seq[Schedule]) []Schedule {
	var schedules []Schedule
	for schedule := range sequence {
		schedules = append(schedules, schedule)
	}
	return schedules
}

func oneWorkerScenario() Scenario {
	value := matchingScenario()
	value.Workers = value.Workers[:1]
	return value
}

func threeWorkerScenario() Scenario {
	value := matchingScenario()
	value.Workers = append(value.Workers, Worker{ID: "w3", Command: "assign"})
	return value
}
