package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/weavegate/weavegate/internal/scenario"
	"github.com/weavegate/weavegate/internal/trace"
)

// ReplayResult summarizes repeated executions of one saved schedule.
type ReplayResult struct {
	ScheduleID   string
	Repeat       int
	Fingerprints map[string]int
	MismatchRuns []int
	Flaky        bool
	Runs         []RunResult
	Elapsed      time.Duration
}

type normalizedRun struct {
	State     string          `json:"state"`
	Terminals trace.Terminals `json:"terminals"`
	Trace     trace.Trace     `json:"trace"`
}

// Replay executes the same validated schedule repeat times. A logical
// fingerprint mismatch is returned as Flaky=true; run and observer failures
// remain errors and are never converted into flakiness.
func (o *Orchestrator) Replay(
	ctx context.Context,
	value scenario.Scenario,
	schedule scenario.Schedule,
	repeat int,
	observer Observer,
) (result ReplayResult, returnErr error) {
	startedAt := time.Now()
	result = ReplayResult{
		ScheduleID:   schedule.ID,
		Repeat:       repeat,
		Fingerprints: make(map[string]int),
	}
	defer func() {
		result.Elapsed = time.Since(startedAt)
	}()

	if repeat < 1 {
		return result, fmt.Errorf("replay schedule %q: repeat must be positive, got %d", schedule.ID, repeat)
	}

	baseline := ""
	for index := 0; index < repeat; index++ {
		run, err := o.Run(ctx, value, schedule, observer)
		if err != nil {
			result.Flaky = false
			return result, fmt.Errorf(
				"replay schedule %q run %d/%d: %w",
				schedule.ID,
				index+1,
				repeat,
				err,
			)
		}

		fingerprint, err := normalizedFingerprint(run)
		if err != nil {
			result.Flaky = false
			return result, fmt.Errorf(
				"replay schedule %q run %d/%d: fingerprint: %w",
				schedule.ID,
				index+1,
				repeat,
				err,
			)
		}
		run.Fingerprint = fingerprint
		result.Runs = append(result.Runs, run)
		result.Fingerprints[fingerprint]++
		if index == 0 {
			baseline = fingerprint
		} else if fingerprint != baseline {
			result.MismatchRuns = append(result.MismatchRuns, index+1)
		}
	}

	result.Flaky = len(result.Fingerprints) > 1
	return result, nil
}

func normalizedFingerprint(run RunResult) (string, error) {
	state := run.StateFingerprint
	if state == "" {
		state = run.Fingerprint
	}
	canonical, err := json.Marshal(normalizedRun{
		State:     state,
		Terminals: run.Terminals.Clone(),
		Trace:     run.Trace.Clone(),
	})
	if err != nil {
		return "", fmt.Errorf("marshal normalized run: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}
