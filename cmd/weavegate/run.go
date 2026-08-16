package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/weavegate/weavegate/internal/ci"
	"github.com/weavegate/weavegate/internal/config"
	"github.com/weavegate/weavegate/internal/fixture"
	"github.com/weavegate/weavegate/internal/oracle"
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

	// The teardown defer is registered before checking Provision's error so
	// that a failed provision still gets a Teardown call: when Provision
	// itself starts a container but a later setup step fails, and the
	// fixture's own cleanup-on-failure attempt also fails, it deliberately
	// retains the container for a subsequent Teardown (see
	// internal/fixture/mysql.go's cleanupFailedProvision) rather than losing
	// track of it. Teardown is a no-op when there is nothing to clean up.
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

	db, err := fx.Provision(ctx, resolved.Fixture)
	if err != nil {
		return reportRunFailure(stderr, ci.FixtureError(fmt.Errorf("run: provision fixture: %w", err)))
	}

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
			Workers:           report.NewWorkers(resolved.Scenario.Workers),
			SyncPoints:        resolved.Scenario.SyncPoints,
			Params:            resolved.Scenario.SUTConfig.Params,
			ViolatingSchedule: report.NewSchedule(outcome.ViolatingSchedule),
		},
		Observation: report.Observation{
			SchedulesExplored:   outcome.SchedulesExplored,
			ExplorePasses:       outcome.PassesExecuted,
			AssertionViolations: assertionViolations(violationEvidenceRuns(outcome)),
			Oracles:             oracleDeclarations(cfg.Oracle.Assertions),
			Repeat:              repeat,
			ViolationRuns:       outcome.Verdict.ViolationRuns,
			Flaky:               outcome.Verdict.Flaky,
			Fingerprints:        outcome.Replay.Fingerprints,
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

// runTrace selects the run whose trace.json should be saved, in priority
// order:
//
//  1. A replay run with an assertion violation: a flaky replay's first run
//     can pass while a later run violates, and observation.json reports
//     that assertion regardless of which run found it, so the saved trace
//     must be able to support the reported observation rather than always
//     coming from a passing run.
//  2. Exploration's own discovery run (the 0/repeat flaky case: exploration
//     found a violation, but every replay run passed) — otherwise a FLAKY
//     verdict would have nothing to show for it at all.
//  3. A replay run whose fingerprint diverged from the others (direct
//     --replay can be flaky purely from execution-fingerprint divergence —
//     differing terminal states or timing classification — with zero
//     assertion violations anywhere; an arbitrary run shows nothing of
//     that divergence, but a mismatching one does).
//  4. The first replay run, when nothing above found anything to prefer.
func runTrace(outcome runOutcome) report.Trace {
	selected, ok := selectTraceRun(outcome)
	if !ok {
		return report.Trace{}
	}
	return report.NewTrace(selected.ScheduleID, selected.Trace, selected.Terminals)
}

func selectTraceRun(outcome runOutcome) (orchestrator.RunResult, bool) {
	for _, run := range outcome.Replay.Runs {
		if runHasViolation(run) {
			return run, true
		}
	}
	if outcome.DiscoveryRun != nil {
		return *outcome.DiscoveryRun, true
	}
	if run, ok := firstMismatchRun(outcome.Replay); ok {
		return run, true
	}
	if len(outcome.Replay.Runs) > 0 {
		return outcome.Replay.Runs[0], true
	}
	return orchestrator.RunResult{}, false
}

// firstMismatchRun returns the first replay run whose fingerprint diverged
// from the replay's baseline (its first run), using orchestrator.Replay's
// own 1-based MismatchRuns indices into Runs.
func firstMismatchRun(replay orchestrator.ReplayResult) (orchestrator.RunResult, bool) {
	if len(replay.MismatchRuns) == 0 {
		return orchestrator.RunResult{}, false
	}
	index := replay.MismatchRuns[0] - 1
	if index < 0 || index >= len(replay.Runs) {
		return orchestrator.RunResult{}, false
	}
	return replay.Runs[index], true
}

func runHasViolation(run orchestrator.RunResult) bool {
	for _, result := range run.Evaluation.Results {
		if len(result.Violations) > 0 {
			return true
		}
	}
	return false
}

// violationEvidenceRuns returns every run that should be scanned for
// violated assertion IDs: all replay runs, plus the exploration's discovery
// run when replay never reproduced its violation. Without the discovery
// run, a 0/repeat flaky verdict would report assertion_violations: [] even
// though exploration found a specific assertion violated.
func violationEvidenceRuns(outcome runOutcome) []orchestrator.RunResult {
	runs := make([]orchestrator.RunResult, 0, len(outcome.Replay.Runs)+1)
	runs = append(runs, outcome.Replay.Runs...)
	if outcome.DiscoveryRun != nil {
		runs = append(runs, *outcome.DiscoveryRun)
	}
	return runs
}

// assertionViolations collects every distinct assertion violation across
// runs, preserving each violation's evidence rows (oracle.Violation.Rows) so
// a saved verdict can be audited against the rows that produced it. Runs are
// scanned in violationEvidenceRuns' order (replay runs, then the discovery
// run), and within a run in oracle declaration order, so the result order is
// deterministic for identical inputs. Two violations sharing an OracleID are
// only collapsed into one entry when their evidence rows are also identical
// — a later run reproducing the same assertion with different rows is kept
// as a separate, individually auditable entry.
func assertionViolations(runs []orchestrator.RunResult) []report.AssertionViolation {
	seen := make(map[string]struct{})
	var violations []report.AssertionViolation
	for _, run := range runs {
		for _, result := range run.Evaluation.Results {
			for _, violation := range result.Violations {
				rows := convertRows(violation.Rows)
				key, err := violationKey(result.OracleID, rows)
				if err != nil {
					continue
				}
				if _, exists := seen[key]; exists {
					continue
				}
				seen[key] = struct{}{}
				violations = append(violations, report.AssertionViolation{
					OracleID: result.OracleID,
					Rows:     rows,
				})
			}
		}
	}
	return violations
}

func convertRows(rows []oracle.Row) []report.Row {
	converted := make([]report.Row, 0, len(rows))
	for _, row := range rows {
		converted = append(converted, report.Row(row))
	}
	return converted
}

func violationKey(oracleID string, rows []report.Row) (string, error) {
	encoded, err := json.Marshal(struct {
		OracleID string       `json:"oracle_id"`
		Rows     []report.Row `json:"rows"`
	}{OracleID: oracleID, Rows: rows})
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// oracleDeclarations converts the configured assertions to the shape saved
// with the run's evidence, so a saved verdict stays auditable against the
// query that produced it even after the referenced config changes.
func oracleDeclarations(assertions []config.Assertion) []report.OracleDeclaration {
	declarations := make([]report.OracleDeclaration, 0, len(assertions))
	for _, assertion := range assertions {
		declarations = append(declarations, report.OracleDeclaration{
			ID:         assertion.ID,
			SQL:        assertion.SQL,
			ExpectRows: assertion.ExpectRows,
		})
	}
	return declarations
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
