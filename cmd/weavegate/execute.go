package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/weavegate/weavegate/internal/ci"
	"github.com/weavegate/weavegate/internal/config"
	"github.com/weavegate/weavegate/internal/orchestrator"
	"github.com/weavegate/weavegate/internal/scenario"
)

// runOutcome is the result of either sweeping a scenario's candidate
// schedules (explore mode) or replaying one given schedule (replay mode).
type runOutcome struct {
	Mode              Mode
	Exhausted         bool
	SchedulesExplored int
	PassesExecuted    int
	ViolatingSchedule *scenario.Schedule
	Replay            orchestrator.ReplayResult

	// DiscoveryRun is the single exploration run that first found a
	// violation (explore mode only; nil in replay mode and when exploration
	// found nothing). It is evidence in its own right: when replay cannot
	// reproduce the violation on any of its repeat runs (the 0/repeat flaky
	// case), it is the only run that actually shows what exploration found.
	DiscoveryRun *orchestrator.RunResult

	Verdict Verdict
}

// executeScenario runs the scenario named in flags: a full explore sweep
// that stops at the first violation and replays it, or — when --replay is
// set — a direct replay of one saved schedule. Orchestrator failures are
// returned as configuration-shaped errors (exit 5): a run or Oracle failure
// is an execution problem, not a verdict, and A-17 forbids turning it into
// flakiness.
func executeScenario(
	ctx context.Context,
	executor *orchestrator.Orchestrator,
	resolved Resolved,
	cfg config.Config,
	repeat int,
	flags runFlags,
) (runOutcome, error) {
	if flags.replay != "" {
		return executeReplay(ctx, executor, resolved, repeat, flags)
	}
	return executeExplore(ctx, executor, resolved, cfg, repeat)
}

func executeReplay(
	ctx context.Context,
	executor *orchestrator.Orchestrator,
	resolved Resolved,
	repeat int,
	flags runFlags,
) (runOutcome, error) {
	schedule, err := resolveReplaySchedule(flags.replay, flags.out, resolved.SchedulesDir)
	if err != nil {
		return runOutcome{}, err
	}
	if err := scenario.Validate(resolved.Scenario, schedule); err != nil {
		return runOutcome{}, ci.InputError(fmt.Errorf("run: replay schedule %q: %w", schedule.ID, err))
	}

	replay, err := executor.Replay(ctx, resolved.Scenario, schedule, repeat, resolved.Oracle)
	if err != nil {
		return runOutcome{}, classifyExecutionError(fmt.Errorf("run: replay schedule %q: %w", schedule.ID, err))
	}

	verdict := ComputeVerdict(VerdictInput{Mode: ModeReplay, Replay: replay})
	return runOutcome{
		Mode:              ModeReplay,
		SchedulesExplored: 0,
		PassesExecuted:    0,
		ViolatingSchedule: &schedule,
		Replay:            replay,
		Verdict:           verdict,
	}, nil
}

func executeExplore(
	ctx context.Context,
	executor *orchestrator.Orchestrator,
	resolved Resolved,
	cfg config.Config,
	repeat int,
) (runOutcome, error) {
	explorePasses := cfg.Run.ExplorePasses
	schedulesExplored := 0

	for pass := 1; pass <= explorePasses; pass++ {
		result, err := executor.Explore(ctx, resolved.Scenario, scenario.Exhaustive{}, resolved.Oracle)
		if err != nil {
			return runOutcome{}, classifyExecutionError(fmt.Errorf("run: explore pass %d/%d: %w", pass, explorePasses, err))
		}
		schedulesExplored += result.Evaluated

		if result.Exhausted {
			continue
		}

		schedule := result.Violating.Clone()
		replay, err := executor.Replay(ctx, resolved.Scenario, schedule, repeat, resolved.Oracle)
		if err != nil {
			return runOutcome{}, classifyExecutionError(fmt.Errorf("run: replay discovered schedule %q: %w", schedule.ID, err))
		}

		verdict := ComputeVerdict(VerdictInput{
			Mode:                 ModeExplore,
			DiscoveryFingerprint: result.ViolatingRun.Fingerprint,
			Replay:               replay,
		})
		discoveryRun := result.ViolatingRun
		return runOutcome{
			Mode:              ModeExplore,
			SchedulesExplored: schedulesExplored,
			PassesExecuted:    pass,
			ViolatingSchedule: &schedule,
			Replay:            replay,
			DiscoveryRun:      &discoveryRun,
			Verdict:           verdict,
		}, nil
	}

	verdict := ComputeVerdict(VerdictInput{Mode: ModeExplore, Exhausted: true})
	return runOutcome{
		Mode:              ModeExplore,
		Exhausted:         true,
		SchedulesExplored: schedulesExplored,
		PassesExecuted:    explorePasses,
		Verdict:           verdict,
	}, nil
}

// classifyExecutionError preserves the orchestrator's own classification of
// an Explore/Replay failure instead of collapsing every one of them to
// ci.InputError: a database container dying or Fixture.Reset otherwise
// failing mid-run is an infrastructure problem (exit 4), not a
// configuration one (exit 5), and CI needs to be able to tell the two
// apart.
func classifyExecutionError(err error) error {
	var fixtureErr *orchestrator.FixtureError
	if errors.As(err, &fixtureErr) {
		return ci.FixtureError(err)
	}
	return ci.InputError(err)
}
