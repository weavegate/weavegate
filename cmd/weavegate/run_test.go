package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weavegate/weavegate/internal/ci"
	"github.com/weavegate/weavegate/internal/fixture"
	"github.com/weavegate/weavegate/internal/orchestrator"
	"github.com/weavegate/weavegate/internal/report"
	"github.com/weavegate/weavegate/internal/scenario"
)

func requireDocker(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping Docker-backed test in -short mode")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("skipping Docker-backed test: docker unavailable: %v", err)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	original, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("change directory to %q: %v", dir, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(original); err != nil {
			t.Fatalf("restore working directory to %q: %v", original, err)
		}
	})
}

func latestRunDir(t *testing.T, outDir string) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(outDir, "runs"))
	if err != nil {
		t.Fatalf("read runs directory under %q: %v", outDir, err)
	}
	if len(entries) != 1 {
		t.Fatalf("runs directory under %q has %d entries, want 1", outDir, len(entries))
	}
	return filepath.Join(outDir, "runs", entries[0].Name())
}

func listRunFiles(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read run directory %q: %v", dir, err)
	}
	return entries
}

func readObservation(t *testing.T, dir string) report.Observation {
	t.Helper()
	var observation report.Observation
	content, err := os.ReadFile(filepath.Join(dir, report.ObservationFile))
	if err != nil {
		t.Fatalf("read %s: %v", report.ObservationFile, err)
	}
	if err := json.Unmarshal(content, &observation); err != nil {
		t.Fatalf("parse %s: %v", report.ObservationFile, err)
	}
	return observation
}

func readManifest(t *testing.T, dir string) report.Manifest {
	t.Helper()
	var manifest report.Manifest
	content, err := os.ReadFile(filepath.Join(dir, report.ManifestFile))
	if err != nil {
		t.Fatalf("read %s: %v", report.ManifestFile, err)
	}
	if err := json.Unmarshal(content, &manifest); err != nil {
		t.Fatalf("parse %s: %v", report.ManifestFile, err)
	}
	return manifest
}

func readScenario(t *testing.T, dir string) report.Scenario {
	t.Helper()
	var doc report.Scenario
	content, err := os.ReadFile(filepath.Join(dir, report.ScenarioFile))
	if err != nil {
		t.Fatalf("read %s: %v", report.ScenarioFile, err)
	}
	if err := json.Unmarshal(content, &doc); err != nil {
		t.Fatalf("parse %s: %v", report.ScenarioFile, err)
	}
	return doc
}

func extractReplayLine(t *testing.T, dir string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(dir, report.MarkdownFile))
	if err != nil {
		t.Fatalf("read %s: %v", report.MarkdownFile, err)
	}
	for _, line := range strings.Split(string(content), "\n") {
		if strings.HasPrefix(line, "replay: ") {
			return strings.TrimPrefix(line, "replay: ")
		}
	}
	t.Fatalf("report.md has no replay line: %s", content)
	return ""
}

type teardownFailingFixture struct {
	fixture.Provisioner
}

func (f teardownFailingFixture) Teardown(ctx context.Context) error {
	_ = f.Provisioner.Teardown(ctx)
	return errors.New("simulated cleanup failure")
}

// resetFailingFixture simulates a database container dying (or Reset
// otherwise failing) partway through exploration or replay: Provision and
// Teardown behave normally, but every Reset call fails.
type resetFailingFixture struct {
	fixture.Provisioner
}

func (f resetFailingFixture) Reset(context.Context) error {
	return errors.New("simulated fixture reset failure")
}

// failingWriter simulates a broken output pipe: every Write call fails,
// as if the process consuming stdout had already closed its end.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("simulated write failure")
}

func TestRun(t *testing.T) {
	requireDocker(t)

	observed := map[string]string{}
	// Computed before any chdir below: repoRoot derives the module root from
	// the current working directory, which only holds while it is still the
	// go test package directory.
	root := repoRoot(t)
	configPath := filepath.Join(root, "fixtures", "matching-slice", ".weavegate", "config.yaml")

	// report.md's replay line omits --out (it is not part of the
	// deterministic file contract; see buildReplayCommand), so a pasted
	// replay resolves stage ① of --replay lookup (A-5) against the default
	// ".weavegate" relative to the *current* working directory. Running the
	// whole test from one fixed directory makes that resolution work the
	// same way a real paste-and-run would.
	workDir := t.TempDir()
	chdir(t, workDir)
	vulnerableOutDir := filepath.Join(workDir, ".weavegate")
	var vulnerableDir string

	t.Run("explore_vulnerable", func(t *testing.T) {
		stdout, stderr, exit := run(
			"run",
			"--config", configPath,
			"--scenario", "concurrent-assign",
			"--variant", "vulnerable",
		)
		if exit != ci.ExitViolation {
			t.Fatalf("vulnerable explore exit = %d, want %d; stdout=%s stderr=%s", exit, ci.ExitViolation, stdout, stderr)
		}
		dir := latestRunDir(t, vulnerableOutDir)
		entries := listRunFiles(t, dir)
		if len(entries) != 6 {
			t.Fatalf("run directory has %d files, want 6", len(entries))
		}
		obs := readObservation(t, dir)
		if obs.Repeat != 20 || obs.ViolationRuns != 20 || obs.Flaky {
			t.Fatalf("vulnerable observation = %+v, want repeat=20 violation_runs=20 flaky=false", obs)
		}
		doc := readScenario(t, dir)
		if doc.Schedule == nil || !strings.HasPrefix(doc.Schedule.ID, "sch_") {
			t.Fatalf("vulnerable scenario.json violating schedule missing/invalid: %+v", doc)
		}
		if obs.Mode != string(ModeExplore) || obs.DiscoveryFingerprint == "" {
			t.Fatalf("vulnerable observation mode/discovery = %q/%q", obs.Mode, obs.DiscoveryFingerprint)
		}
		if !strings.Contains(stdout, "## weavegate: FAIL") {
			t.Fatalf("vulnerable stdout = %q, want the report.md headline", stdout)
		}

		vulnerableDir = dir

		t.Logf(
			"CLI_RUN_RESULT mode=explore variant=vulnerable passes=%d evaluated=%d schedule=%s repeat=%d violation_runs=%d flaky=%t artifacts=%d exit=%d",
			obs.ExplorePasses, obs.SchedulesExplored, doc.Schedule.ID, obs.Repeat, obs.ViolationRuns, obs.Flaky, len(entries), exit,
		)
	})

	t.Run("explore_fixed", func(t *testing.T) {
		outDir := t.TempDir()
		stdout, stderr, exit := run(
			"run",
			"--config", configPath,
			"--scenario", "concurrent-assign",
			"--variant", "fixed",
			"--out", outDir,
		)
		if exit != ci.ExitOK {
			t.Fatalf("fixed explore exit = %d, want %d; stdout=%s stderr=%s", exit, ci.ExitOK, stdout, stderr)
		}
		dir := latestRunDir(t, outDir)
		entries := listRunFiles(t, dir)
		if len(entries) != 6 {
			t.Fatalf("run directory has %d files, want 6", len(entries))
		}
		obs := readObservation(t, dir)
		if obs.SchedulesExplored != 18 || obs.ExplorePasses != 3 || obs.Flaky || obs.ViolationRuns != 0 {
			t.Fatalf("fixed observation = %+v, want evaluated=18 passes=3 flaky=false violation_runs=0", obs)
		}
		doc := readScenario(t, dir)
		if doc.Schedule != nil {
			t.Fatalf("fixed scenario.json has a schedule, want none: %+v", doc.Schedule)
		}
		if obs.Mode != string(ModeExplore) || obs.DiscoveryFingerprint != "" {
			t.Fatalf("fixed observation mode/discovery = %q/%q", obs.Mode, obs.DiscoveryFingerprint)
		}

		observed["artifacts_written_on_pass"] = "true"

		t.Logf(
			"CLI_RUN_RESULT mode=explore variant=fixed passes=%d evaluated=%d violating=none exhausted=true flaky=%t artifacts=%d exit=%d",
			obs.ExplorePasses, obs.SchedulesExplored, obs.Flaky, len(entries), exit,
		)
	})

	t.Run("replay_known_schedule", func(t *testing.T) {
		// The registry's schedules directory is a repo-relative path
		// (A-3): run from the repo root so stage ② of A-5 resolves it.
		chdir(t, root)

		outDir := t.TempDir()
		stdout, stderr, exit := run(
			"run",
			"--config", configPath,
			"--scenario", "concurrent-assign",
			"--variant", "vulnerable",
			"--replay", "sch_ba00582f9632",
			"--repeat", "20",
			"--out", outDir,
		)
		if exit != ci.ExitViolation {
			t.Fatalf("replay known schedule exit = %d, want %d; stdout=%s stderr=%s", exit, ci.ExitViolation, stdout, stderr)
		}
		dir := latestRunDir(t, outDir)
		entries := listRunFiles(t, dir)
		obs := readObservation(t, dir)
		doc := readScenario(t, dir)
		if doc.Schedule == nil || doc.Schedule.ID != "sch_ba00582f9632" {
			t.Fatalf("replay schedule = %+v, want sch_ba00582f9632", doc.Schedule)
		}
		if obs.Mode != string(ModeReplay) || obs.DiscoveryFingerprint != "" {
			t.Fatalf("replay observation mode/discovery = %q/%q", obs.Mode, obs.DiscoveryFingerprint)
		}

		t.Logf(
			"CLI_RUN_RESULT mode=replay variant=vulnerable schedule=%s repeat=%d violation_runs=%d flaky=%t artifacts=%d exit=%d",
			doc.Schedule.ID, obs.Repeat, obs.ViolationRuns, obs.Flaky, len(entries), exit,
		)
	})

	t.Run("replay_command_roundtrip", func(t *testing.T) {
		if vulnerableDir == "" {
			t.Fatal("vulnerable run directory not captured; explore_vulnerable must run first")
		}
		replayLine := extractReplayLine(t, vulnerableDir)
		fields := strings.Fields(replayLine)
		if len(fields) < 2 || fields[0] != "weavegate" || fields[1] != "run" {
			t.Fatalf("replay line = %q, does not start with \"weavegate run\"", replayLine)
		}

		stdout, stderr, exit := run(fields[1:]...)
		if exit != ci.ExitViolation {
			t.Fatalf("replay command roundtrip exit = %d, want %d; stdout=%s stderr=%s", exit, ci.ExitViolation, stdout, stderr)
		}

		t.Logf("CLI_REPLAY_COMMAND_RESULT source=report_md executed=verbatim resolved_from=run_directory verdict=identical exit=%d", exit)
	})

	t.Run("bad_config", func(t *testing.T) {
		_, _, exit := run("run", "--config", filepath.Join(t.TempDir(), "does-not-exist.yaml"), "--scenario", "concurrent-assign")
		if exit != ci.ExitInput {
			t.Fatalf("bad config exit = %d, want %d", exit, ci.ExitInput)
		}
		observed["bad_config"] = "5"
	})

	t.Run("missing_scenario", func(t *testing.T) {
		_, _, exit := run("run", "--config", configPath)
		if exit != ci.ExitInput {
			t.Fatalf("missing --scenario exit = %d, want %d", exit, ci.ExitInput)
		}
		observed["missing_scenario"] = "5"
	})

	t.Run("unknown_scenario", func(t *testing.T) {
		_, _, exit := run("run", "--config", configPath, "--scenario", "does-not-exist")
		if exit != ci.ExitInput {
			t.Fatalf("unknown scenario exit = %d, want %d", exit, ci.ExitInput)
		}
		observed["unknown_scenario"] = "5"
	})

	t.Run("nonpositive_repeat_override", func(t *testing.T) {
		// Neither Docker nor a valid --scenario is reachable before this
		// rejection: it must fire from flag validation alone, before
		// provisioning starts.
		_, _, exit := run("run", "--config", configPath, "--scenario", "concurrent-assign", "--repeat", "0")
		if exit != ci.ExitInput {
			t.Fatalf("--repeat 0 exit = %d, want %d", exit, ci.ExitInput)
		}
		_, _, exit = run("run", "--config", configPath, "--scenario", "concurrent-assign", "--repeat", "-1")
		if exit != ci.ExitInput {
			t.Fatalf("--repeat -1 exit = %d, want %d", exit, ci.ExitInput)
		}
		observed["nonpositive_repeat_override"] = "5"
	})

	t.Run("unknown_schedule", func(t *testing.T) {
		_, _, exit := run(
			"run", "--config", configPath, "--scenario", "concurrent-assign",
			"--replay", "sch_000000000000", "--out", t.TempDir(),
		)
		if exit != ci.ExitInput {
			t.Fatalf("unknown schedule exit = %d, want %d", exit, ci.ExitInput)
		}
		observed["unknown_schedule"] = "5"
	})

	t.Run("ambiguous_schedule", func(t *testing.T) {
		// resolveReplaySchedule is a pure filesystem lookup; exercise it
		// directly rather than through a full Docker-backed run.
		outDir := t.TempDir()
		runDirA := filepath.Join(outDir, "runs", "run_a")
		runDirB := filepath.Join(outDir, "runs", "run_b")
		if err := os.MkdirAll(runDirA, 0o755); err != nil {
			t.Fatalf("create run dir A: %v", err)
		}
		if err := os.MkdirAll(runDirB, 0o755); err != nil {
			t.Fatalf("create run dir B: %v", err)
		}

		scheduleA, err := scenario.NewSchedule([]scenario.CoordinationStep{{Worker: "w1", Point: "p1"}})
		if err != nil {
			t.Fatalf("build schedule A: %v", err)
		}
		scheduleB, err := scenario.NewSchedule([]scenario.CoordinationStep{{Worker: "w2", Point: "p2"}})
		if err != nil {
			t.Fatalf("build schedule B: %v", err)
		}
		// Force the same ID onto conflicting content, simulating tampered
		// or corrupted saved evidence.
		scheduleB.ID = scheduleA.ID

		writeScenarioDoc(t, filepath.Join(runDirA, "scenario.json"), scheduleA)
		writeScenarioDoc(t, filepath.Join(runDirB, "scenario.json"), scheduleB)

		_, err = resolveReplaySchedule(scheduleA.ID, outDir, nil)
		if err == nil {
			t.Fatal("resolve ambiguous schedule: want error, got nil")
		}
		if got := ci.ExitCode(err, ci.Verdict{}); got != ci.ExitInput {
			t.Fatalf("ambiguous schedule exit = %d, want %d", got, ci.ExitInput)
		}
		observed["ambiguous_schedule"] = "5"
	})

	t.Run("missing_fixture_source", func(t *testing.T) {
		badConfig := writeConfigWithBadMigrations(t, configPath)
		_, _, exit := run("run", "--config", badConfig, "--scenario", "concurrent-assign", "--out", t.TempDir())
		if exit != ci.ExitInput {
			t.Fatalf("missing fixture source exit = %d, want %d", exit, ci.ExitInput)
		}
		observed["missing_fixture_source"] = "5"
	})

	t.Run("unwritable_out", func(t *testing.T) {
		blocked := filepath.Join(t.TempDir(), "blocked")
		if err := os.WriteFile(blocked, []byte("not a directory"), 0o644); err != nil {
			t.Fatalf("create blocking file: %v", err)
		}
		_, _, exit := run(
			"run", "--config", configPath, "--scenario", "concurrent-assign",
			"--variant", "fixed", "--out", filepath.Join(blocked, "out"),
		)
		if exit != ci.ExitInput {
			t.Fatalf("unwritable --out exit = %d, want %d", exit, ci.ExitInput)
		}
		observed["unwritable_out"] = "5"
	})

	t.Run("cleanup_failure_on_pass", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		outDir := t.TempDir()
		flags := runFlags{
			config:   configPath,
			scenario: "concurrent-assign",
			variant:  "fixed",
			out:      outDir,
		}
		err := runScenario(context.Background(), &stdout, &stderr, flags, func() fixture.Provisioner {
			return teardownFailingFixture{Provisioner: fixture.NewMySQLFixture()}
		})
		exit := exitCodeFromError(err)
		if exit != ci.ExitFixture {
			t.Fatalf("cleanup failure on pass exit = %d, want %d; stderr=%s", exit, ci.ExitFixture, stderr.String())
		}
		if !strings.Contains(stderr.String(), "cleanup failed") {
			t.Fatalf("cleanup failure stderr = %q, want a cleanup warning", stderr.String())
		}
		if !strings.Contains(stdout.String(), "## weavegate: PASS") {
			t.Fatalf("cleanup failure stdout = %q, want the scenario's own PASS headline unaffected by cleanup", stdout.String())
		}
		manifest := readManifest(t, latestRunDir(t, outDir))
		if !manifest.CleanupFailed {
			t.Fatalf("saved manifest.json cleanup_failed = false, want true so the run directory itself records the leaked container")
		}
		observed["cleanup_failure_on_pass"] = "4"
	})

	t.Run("fixture_failure_during_replay", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		flags := runFlags{
			config:   configPath,
			scenario: "concurrent-assign",
			variant:  "vulnerable",
			out:      t.TempDir(),
		}
		err := runScenario(context.Background(), &stdout, &stderr, flags, func() fixture.Provisioner {
			return resetFailingFixture{Provisioner: fixture.NewMySQLFixture()}
		})
		exit := exitCodeFromError(err)
		if exit != ci.ExitFixture {
			t.Fatalf("fixture failure during replay exit = %d, want %d; stderr=%s", exit, ci.ExitFixture, stderr.String())
		}
		observed["fixture_failure_during_replay"] = "4"
	})

	t.Run("stdout_write_failure", func(t *testing.T) {
		var stderr bytes.Buffer
		flags := runFlags{
			config:   configPath,
			scenario: "concurrent-assign",
			variant:  "fixed",
			out:      t.TempDir(),
		}
		err := runScenario(context.Background(), failingWriter{}, &stderr, flags, fixture.NewMySQLFixture)
		exit := exitCodeFromError(err)
		if exit != ci.ExitInput {
			t.Fatalf("stdout write failure exit = %d, want %d; stderr=%s", exit, ci.ExitInput, stderr.String())
		}
		observed["stdout_write_failure"] = "5"
	})

	order := []string{
		"bad_config", "missing_scenario", "unknown_scenario", "nonpositive_repeat_override", "unknown_schedule",
		"ambiguous_schedule", "unwritable_out", "missing_fixture_source",
		"artifacts_written_on_pass", "cleanup_failure_on_pass", "fixture_failure_during_replay", "stdout_write_failure",
	}
	parts := make([]string, 0, len(order))
	for _, key := range order {
		value, ok := observed[key]
		if !ok {
			t.Fatalf("marker attribute %q was not observed by any subtest", key)
		}
		parts = append(parts, key+"="+value)
	}
	t.Logf("CLI_RUN_FAILURE_RESULT %s", strings.Join(parts, " "))
}

func writeScenarioDoc(t *testing.T, path string, schedule scenario.Schedule) {
	t.Helper()
	doc := struct {
		ViolatingSchedule scenario.Schedule `json:"violating_schedule"`
	}{ViolatingSchedule: schedule}
	content, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal scenario document: %v", err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write scenario document %q: %v", path, err)
	}
}

func writeConfigWithBadMigrations(t *testing.T, configPath string) string {
	t.Helper()
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config %q: %v", configPath, err)
	}
	mutated := strings.Replace(string(content), "migrations: ../db/migration", "migrations: ../db/does-not-exist", 1)
	if mutated == string(content) {
		t.Fatal("write bad-migrations config: expected substring not found in source config")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(mutated), 0o644); err != nil {
		t.Fatalf("write bad-migrations config: %v", err)
	}
	return path
}

// TestRunEvidenceSelection covers the 0/repeat flaky case (A-18-adjacent):
// exploration finds a violation, but every replay run passes. Without a
// discovery-run fallback, runTrace and assertionViolations would report
// nothing to substantiate the resulting FLAKY verdict. No Docker needed:
// runOutcome is built directly, the same way verdict_test.go builds
// VerdictInput.
func TestRunEvidenceSelection(t *testing.T) {
	violating := violatingEvaluation(t)
	passing := passingEvaluation(t)
	discoveryRun := orchestrator.RunResult{
		ScheduleID:  "sch_discovery000",
		Evaluation:  violating,
		Fingerprint: "fp-violation",
		Trace:       nil,
	}

	t.Run("discovery_fallback_when_replay_never_reproduces", func(t *testing.T) {
		outcome := runOutcome{
			Mode:         ModeExplore,
			DiscoveryRun: &discoveryRun,
			Replay: orchestrator.ReplayResult{
				Runs: repeatRuns(passing, "fp-pass", 20),
			},
		}

		trace := runTrace(outcome)
		if trace.ScheduleRef != discoveryRun.ScheduleID {
			t.Fatalf("trace.schedule_ref = %q, want discovery run %q", trace.ScheduleRef, discoveryRun.ScheduleID)
		}

		violations := assertionViolations(violationEvidenceRuns(outcome))
		if len(violations) != 1 || violations[0].OracleID != "active-assignment-is-unique" {
			t.Fatalf("assertion_violations = %+v, want [active-assignment-is-unique] from the discovery run", violations)
		}
		if len(violations[0].Rows) == 0 {
			t.Fatalf("assertion_violations[0].Rows is empty, want the discovery run's evidence rows")
		}
	})

	t.Run("violating_replay_run_preferred_over_discovery", func(t *testing.T) {
		outcome := runOutcome{
			Mode:         ModeExplore,
			DiscoveryRun: &discoveryRun,
			Replay: orchestrator.ReplayResult{
				Runs: append(repeatRuns(passing, "fp-pass", 19), orchestrator.RunResult{
					ScheduleID:  "sch_replayrun0001",
					Evaluation:  violating,
					Fingerprint: "fp-violation",
				}),
			},
		}

		trace := runTrace(outcome)
		if trace.ScheduleRef != "sch_replayrun0001" {
			t.Fatalf("trace.schedule_ref = %q, want the violating replay run, not the discovery run", trace.ScheduleRef)
		}
	})

	t.Run("mismatch_run_preferred_when_no_violation_anywhere", func(t *testing.T) {
		// Direct --replay can be flaky purely from fingerprint divergence
		// (differing terminal states or timing classification) with zero
		// assertion violations anywhere -- no discovery run exists in this
		// mode, so the mismatching run is the only informative choice.
		baseline := orchestrator.RunResult{ScheduleID: "sch_baseline0001", Evaluation: passing, Fingerprint: "fp-a"}
		mismatch := orchestrator.RunResult{ScheduleID: "sch_mismatch0001", Evaluation: passing, Fingerprint: "fp-b"}
		outcome := runOutcome{
			Mode: ModeReplay,
			Replay: orchestrator.ReplayResult{
				Fingerprints: map[string]int{"fp-a": 1, "fp-b": 1},
				MismatchRuns: []int{2},
				Runs:         []orchestrator.RunResult{baseline, mismatch},
			},
		}

		trace := runTrace(outcome)
		if trace.ScheduleRef != mismatch.ScheduleID {
			t.Fatalf("trace.schedule_ref = %q, want the mismatching run %q, not the baseline", trace.ScheduleRef, mismatch.ScheduleID)
		}
	})

	t.Run("no_discovery_falls_back_to_first_replay_run", func(t *testing.T) {
		outcome := runOutcome{
			Mode: ModeExplore,
			Replay: orchestrator.ReplayResult{
				Runs: repeatRuns(passing, "fp-pass", 3),
			},
		}

		trace := runTrace(outcome)
		if trace.ScheduleRef == discoveryRun.ScheduleID {
			t.Fatalf("trace.schedule_ref = %q, want a replay run (no discovery run set)", trace.ScheduleRef)
		}
		if len(assertionViolations(violationEvidenceRuns(outcome))) != 0 {
			t.Fatalf("assertion_violations should be empty when nothing violated")
		}
	})

	t.Run("no_runs_at_all", func(t *testing.T) {
		trace := runTrace(runOutcome{})
		if trace.ScheduleRef != "" || trace.Events != nil || trace.Terminals != nil {
			t.Fatalf("trace = %+v, want zero value when there is nothing to report", trace)
		}
	})
}
