package main

import (
	"fmt"
	"strings"

	"github.com/weavegate/weavegate/internal/ci"
	"github.com/weavegate/weavegate/internal/config"
	"github.com/weavegate/weavegate/internal/scenario"
)

// runPlan is the immutable CLI-side boundary between validating inputs and
// starting fixture side effects. Every schedule and candidate-plan decision
// that can fail because of user input is completed before the fixture factory
// is called.
type runPlan struct {
	Config         config.Config
	Resolved       Resolved
	Mode           Mode
	ReplaySchedule *scenario.Schedule
	CandidatePlan  *scenario.SchedulePlan
	Repeat         int
	Out            string
}

func buildRunPlan(flags runFlags) (runPlan, error) {
	if strings.TrimSpace(flags.config) == "" {
		return runPlan{}, ci.InputError(fmt.Errorf("run: --config must not be empty"))
	}
	if strings.TrimSpace(flags.scenario) == "" {
		return runPlan{}, ci.InputError(fmt.Errorf("run: --scenario must not be empty"))
	}
	if flags.variantSet && strings.TrimSpace(flags.variant) == "" {
		return runPlan{}, ci.InputError(fmt.Errorf("run: --variant must not be empty when specified"))
	}
	if flags.replaySet && strings.TrimSpace(flags.replay) == "" {
		return runPlan{}, ci.InputError(fmt.Errorf("run: --replay must not be empty when specified"))
	}
	if flags.repeatSet && flags.repeat < 1 {
		return runPlan{}, ci.InputError(fmt.Errorf("run: --repeat must be positive, got %d", flags.repeat))
	}
	if strings.TrimSpace(flags.out) == "" {
		return runPlan{}, ci.InputError(fmt.Errorf("run: --out must not be empty"))
	}

	cfg, err := config.Load(flags.config)
	if err != nil {
		return runPlan{}, ci.InputError(fmt.Errorf("run: %w", err))
	}

	resolved, err := Resolve(cfg, flags.scenario, flags.variant)
	if err != nil {
		return runPlan{}, err
	}

	repeat := cfg.Run.Repeat
	if flags.repeatSet {
		repeat = flags.repeat
	}

	plan := runPlan{
		Config:   cfg,
		Resolved: resolved,
		Repeat:   repeat,
		Out:      flags.out,
	}
	if flags.replaySet {
		schedule, err := resolveReplaySchedule(flags.replay, flags.out, resolved.SchedulesDir)
		if err != nil {
			return runPlan{}, err
		}
		if err := scenario.Validate(resolved.Scenario, schedule); err != nil {
			return runPlan{}, ci.InputError(fmt.Errorf("run: replay schedule %q: %w", schedule.ID, err))
		}
		plan.Mode = ModeReplay
		plan.ReplaySchedule = &schedule
		return plan, nil
	}

	candidates, err := (scenario.Exhaustive{}).Schedules(resolved.Scenario)
	if err != nil {
		return runPlan{}, ci.InputError(fmt.Errorf("run: build candidate plan: %w", err))
	}
	if !candidates.TotalKnown || candidates.Total < 1 {
		return runPlan{}, ci.InputError(fmt.Errorf("run: candidate plan must declare a positive total"))
	}
	if candidates.Seq == nil {
		return runPlan{}, ci.InputError(fmt.Errorf("run: candidate plan sequence is required"))
	}
	plan.Mode = ModeExplore
	plan.CandidatePlan = &candidates
	return plan, nil
}
