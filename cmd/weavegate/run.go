package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/weavegate/weavegate/internal/ci"
	"github.com/weavegate/weavegate/internal/config"
	"github.com/weavegate/weavegate/internal/fixture"
	"github.com/weavegate/weavegate/internal/orchestrator"
	"github.com/weavegate/weavegate/internal/report"
	"github.com/weavegate/weavegate/internal/scenario"
)

type runFlags struct {
	config   string
	scenario string
	replay   string
	repeat   int
	variant  string
	out      string
}

func newRunCommand(stdout, stderr io.Writer) *cobra.Command {
	flags := &runFlags{}
	cmd := &cobra.Command{
		Use:           "run",
		Short:         "Reach a verdict on a configured scenario.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runScenario(context.Background(), stdout, stderr, *flags, fixture.NewMySQLFixture)
		},
	}

	cmd.Flags().StringVar(&flags.config, "config", ".weavegate/config.yaml", "path to the run configuration file")
	cmd.Flags().StringVar(&flags.scenario, "scenario", "", "scenario name to run (required)")
	cmd.Flags().StringVar(&flags.replay, "replay", "", "schedule ID or file to replay instead of exploring")
	cmd.Flags().IntVar(&flags.repeat, "repeat", 0, "override run.repeat from the config")
	cmd.Flags().StringVar(&flags.variant, "variant", "", "override target.sut.variant from the config")
	cmd.Flags().StringVar(&flags.out, "out", ".weavegate", "run directory base")
	_ = cmd.MarkFlagRequired("scenario")

	return cmd
}

// runScenario is the run command's implementation, factored out from Cobra
// wiring so tests can inject a fixture factory and call it in-process
// without spawning the compiled binary (os.Exit is never exercised here).
func runScenario(
	ctx context.Context,
	stdout, stderr io.Writer,
	flags runFlags,
	newFixture func() fixture.Fixture,
) (finalErr error) {
	startedAt := time.Now().UTC()

	cfg, err := config.Load(flags.config)
	if err != nil {
		return reportRunFailure(stderr, ci.InputError(fmt.Errorf("run: %w", err)))
	}

	resolved, err := Resolve(cfg, flags.scenario, flags.variant)
	if err != nil {
		return reportRunFailure(stderr, err)
	}

	repeat := cfg.Run.Repeat
	if flags.repeat > 0 {
		repeat = flags.repeat
	}

	fx := newFixture()
	db, err := fx.Provision(ctx, resolved.Fixture)
	if err != nil {
		return reportRunFailure(stderr, ci.FixtureError(fmt.Errorf("run: provision fixture: %w", err)))
	}

	defer func() {
		cleanupErr := fx.Teardown(context.WithoutCancel(ctx))
		if cleanupErr == nil {
			return
		}
		fmt.Fprintf(stderr, "weavegate: warning: cleanup failed: %v\n", cleanupErr)

		// A-18: cleanup failure never lowers an already decided code; it only
		// raises an otherwise passing run to 4, so a leaked container is never
		// reported as PASS and a real violation is never hidden by teardown.
		var exit *exitError
		if errors.As(finalErr, &exit) && exit.code == ci.ExitOK {
			finalErr = &exitError{code: ci.ExitFixture, err: exit.err}
		}
	}()

	runID, err := newRunID(startedAt)
	if err != nil {
		return reportRunFailure(stderr, ci.OutputError(fmt.Errorf("run: %w", err)))
	}

	manifest, err := collectManifest(
		ctx, db, resolved.Fixture,
		cfg.Target.SUT.Adapter, resolved.Scenario.SUTConfig.Variant,
		runID, startedAt,
	)
	if err != nil {
		return reportRunFailure(stderr, ci.FixtureError(fmt.Errorf("run: collect manifest: %w", err)))
	}

	executor, err := orchestrator.New(orchestrator.Config{
		Fixture:               fx,
		DB:                    db,
		NewRuntime:            resolved.NewRuntime,
		NewAdapter:            resolved.NewAdapter,
		BlockInferenceTimeout: resolved.Timeouts.BlockInference,
		StepTimeout:           resolved.Timeouts.Step,
		RunTimeout:            resolved.Timeouts.Run,
		StopTimeout:           resolved.Timeouts.Stop,
	})
	if err != nil {
		return reportRunFailure(stderr, ci.InputError(fmt.Errorf("run: create orchestrator: %w", err)))
	}

	outcome, err := executeScenario(ctx, executor, resolved, cfg, repeat, flags)
	if err != nil {
		return reportRunFailure(stderr, err)
	}

	replayCommand := buildReplayCommand(flags, resolved.Scenario.SUTConfig.Variant, repeat, outcome.ViolatingSchedule)

	run := report.Run{
		Manifest: manifest,
		Scenario: report.Scenario{
			Name:              resolved.Scenario.Name,
			Workers:           resolved.Scenario.Workers,
			SyncPoints:        resolved.Scenario.SyncPoints,
			ViolatingSchedule: outcome.ViolatingSchedule,
		},
		Observation: report.Observation{
			SchedulesExplored:   outcome.SchedulesExplored,
			ExplorePasses:       outcome.PassesExecuted,
			AssertionViolations: violatedAssertionIDs(outcome.Replay.Runs),
			Repeat:              repeat,
			ViolationRuns:       outcome.Verdict.ViolationRuns,
			Flaky:               outcome.Verdict.Flaky,
		},
		Trace:         runTrace(outcome),
		Pass:          outcome.Verdict.ExitCode == ci.ExitOK,
		Flaky:         outcome.Verdict.Flaky,
		ReplayCommand: replayCommand,
	}

	dir, err := report.WriteRun(flags.out, run)
	if err != nil {
		return reportRunFailure(stderr, err)
	}

	content, readErr := os.ReadFile(filepath.Join(dir, report.MarkdownFile))
	if readErr == nil {
		stdout.Write(content)
	}
	fmt.Fprintln(stdout, dir)

	return &exitError{code: outcome.Verdict.ExitCode}
}

func runTrace(outcome runOutcome) report.Trace {
	if len(outcome.Replay.Runs) == 0 {
		return report.Trace{}
	}
	first := outcome.Replay.Runs[0]
	return report.Trace{
		ScheduleRef: first.ScheduleID,
		Events:      first.Trace,
		Terminals:   first.Terminals,
	}
}

func violatedAssertionIDs(runs []orchestrator.RunResult) []string {
	seen := make(map[string]struct{})
	var ids []string
	for _, run := range runs {
		for _, result := range run.Evaluation.Results {
			if len(result.Violations) == 0 {
				continue
			}
			if _, exists := seen[result.OracleID]; exists {
				continue
			}
			seen[result.OracleID] = struct{}{}
			ids = append(ids, result.OracleID)
		}
	}
	sort.Strings(ids)
	return ids
}

// buildReplayCommand renders the self-sufficient replay line (A-5). --out is
// deliberately omitted: it is not part of the deterministic file contract
// (docs/adr/0005-volatile-run-metadata-boundary.md), so a config-as-given
// value here would make report.md vary with where a run happened to write
// its evidence rather than with what was actually run. A reader replaying
// from the printed line uses the same --out (or its default) as the run
// that discovered the schedule, so stage ① of --replay resolution still
// finds it; see cli.md for the two-stage resolution order.
func buildReplayCommand(flags runFlags, variant string, repeat int, schedule *scenario.Schedule) string {
	if schedule == nil {
		return ""
	}
	return fmt.Sprintf(
		"weavegate run --config %s --scenario %s --variant %s --replay %s --repeat %d",
		flags.config,
		flags.scenario,
		variant,
		schedule.ID,
		repeat,
	)
}

// reportRunFailure prints err to stderr and wraps it as the process's
// terminal outcome. It never masks the underlying classification: err is
// expected to already be a ci.FixtureError, ci.InputError, or
// ci.OutputError from the call site.
func reportRunFailure(stderr io.Writer, err error) error {
	fmt.Fprintf(stderr, "weavegate: %v\n", err)
	return &exitError{code: ci.ExitCode(err, ci.Verdict{}), err: err}
}
