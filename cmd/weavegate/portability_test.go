package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weavegate/weavegate/internal/ci"
	"github.com/weavegate/weavegate/internal/report"
)

func TestReplayPortability(t *testing.T) {
	requireDocker(t)

	root := repoRoot(t)
	configPath := filepath.Join(root, "fixtures", "matching-slice", ".weavegate", "config.yaml")
	producerOut := t.TempDir()

	stdout, stderr, exit := run(
		"run",
		"--config", configPath,
		"--scenario", "concurrent-assign",
		"--variant", "vulnerable",
		"--out", producerOut,
	)
	if exit != ci.ExitViolation {
		t.Fatalf("producer explore exit = %d, want %d; stdout=%s stderr=%s", exit, ci.ExitViolation, stdout, stderr)
	}
	producerRunDir := latestRunDir(t, producerOut)
	producerScenario := readScenario(t, producerRunDir)
	producerObservation := readObservation(t, producerRunDir)
	if producerScenario.Schedule == nil {
		t.Fatal("producer run has no discovered schedule")
	}
	if len(producerObservation.Diagnostics) != 1 || producerObservation.Diagnostics[0].Code != "RG001" ||
		len(producerObservation.AssertionViolations) == 0 {
		t.Fatalf("producer verdict evidence = %+v, want RG001 with an assertion violation", producerObservation)
	}

	replayLine := extractReplayLine(t, producerRunDir)
	argv := parseShellReplayArgs(t, replayLine)
	if len(argv) < 2 || argv[0] != "weavegate" || argv[1] != "run" {
		t.Fatalf("replay line = %q, does not start with weavegate run", replayLine)
	}
	if strings.Contains(replayLine, "--out") {
		t.Fatalf("replay line unexpectedly contains --out: %q", replayLine)
	}

	reader := t.TempDir()
	readerOut := filepath.Join(reader, ".weavegate")
	readerSchedules := filepath.Join(readerOut, "schedules")
	if err := os.MkdirAll(readerSchedules, 0o700); err != nil {
		t.Fatalf("create reader schedules directory: %v", err)
	}
	scheduleContent, err := os.ReadFile(filepath.Join(producerRunDir, report.ScheduleFile))
	if err != nil {
		t.Fatalf("read producer schedule.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(readerSchedules, report.ScheduleFile), scheduleContent, 0o600); err != nil {
		t.Fatalf("copy schedule.json to reader: %v", err)
	}
	if _, err := os.Stat(filepath.Join(readerOut, "runs")); !os.IsNotExist(err) {
		t.Fatalf("reader unexpectedly has producer run evidence before replay: %v", err)
	}

	chdir(t, reader)
	replayStdout, replayStderr, replayExit := run(argv[1:]...)
	if replayExit != ci.ExitViolation {
		t.Fatalf("portable replay exit = %d, want %d; stdout=%s stderr=%s", replayExit, ci.ExitViolation, replayStdout, replayStderr)
	}
	if !strings.Contains(replayStdout, "## weavegate: FAIL (RG001)") || !strings.Contains(replayStdout, "error[RG001]:") {
		t.Fatalf("portable replay stdout = %q, want RG001 verdict", replayStdout)
	}
	replayRunDir := latestRunDir(t, readerOut)
	replayObservation := readObservation(t, replayRunDir)
	if len(replayObservation.Diagnostics) != 1 || replayObservation.Diagnostics[0].Code != "RG001" ||
		len(replayObservation.AssertionViolations) == 0 {
		t.Fatalf("portable replay evidence = %+v, want RG001 with an assertion violation", replayObservation)
	}
	if replayObservation.AssertionViolations[0].OracleID != producerObservation.AssertionViolations[0].OracleID {
		t.Fatalf(
			"portable replay assertion = %q, want producer assertion %q",
			replayObservation.AssertionViolations[0].OracleID,
			producerObservation.AssertionViolations[0].OracleID,
		)
	}

	missingReader := t.TempDir()
	chdir(t, missingReader)
	_, missingStderr, missingExit := run(argv[1:]...)
	if missingExit != ci.ExitInput {
		t.Fatalf("missing portable schedule exit = %d, want %d; stderr=%s", missingExit, ci.ExitInput, missingStderr)
	}
	for _, location := range []string{".weavegate", filepath.Join(".weavegate", "schedules"), "embedded schedules"} {
		if !strings.Contains(missingStderr, location) {
			t.Fatalf("missing schedule error %q does not name %q", missingStderr, location)
		}
	}

	t.Log("CLI_REPLAY_PORTABLE_RESULT source=report_md executed=verbatim run_dir=absent schedules_dir=present out=omitted verdict=identical diagnostic=RG001 exit=2 missing_schedule=5")
}
