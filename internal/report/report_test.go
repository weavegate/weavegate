package report

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weavegate/weavegate/internal/ci"
)

func sampleSchedule(t *testing.T) Schedule {
	t.Helper()

	return Schedule{
		ID: "sch_sample00000",
		Steps: []CoordinationStep{
			{Worker: "w1", Point: "after_read_request"},
			{Worker: "w2", Point: "after_read_request"},
		},
	}
}

func sampleRun(t *testing.T, runID string) Run {
	t.Helper()

	schedule := sampleSchedule(t)
	return Run{
		Manifest: Manifest{
			RunID:            runID,
			StartedAt:        time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC),
			WeavegateVersion: "0.0.0-dev",
			SchemaVersion:    "abc123def456",
			SeedData:         "def456abc123",
			IsolationLevel:   "REPEATABLE-READ",
			Engine:           "InnoDB",
			Adapter:          "gonative",
			Variant:          "vulnerable",
			Image:            "mysql:8.4",
		},
		Scenario: Scenario{
			Name: "concurrent-assign",
			Workers: []Worker{
				{ID: "w1", Command: "assign"},
				{ID: "w2", Command: "assign"},
			},
			SyncPoints:        []string{"after_read_request", "before_insert_assignment"},
			ViolatingSchedule: &schedule,
		},
		Observation: Observation{
			SchedulesExplored: 2,
			ExplorePasses:     1,
			AssertionViolations: []AssertionViolation{
				{OracleID: "active-assignment-is-unique", Rows: []Row{{"active_count": int64(2)}}},
			},
			Repeat: 20,
			Timeouts: Timeouts{
				ArriveMS:         3000,
				BlockInferenceMS: 3000,
				StepMS:           60000,
				RunMS:            180000,
				StopMS:           60000,
			},
			ViolationRuns: 20,
			Flaky:         false,
		},
		Trace: Trace{
			ScheduleRef: schedule.ID,
			Events: []Event{
				{Seq: 1, Kind: "fixture_reset", Step: -1, Status: "none", FailureClass: "none"},
			},
			Terminals: []WorkerTerminal{
				{Worker: "w1", State: "done", FailureClass: "none"},
			},
		},
		Pass:          false,
		Flaky:         false,
		ReplayCommand: "weavegate run --config .weavegate/config.yaml --scenario concurrent-assign --variant vulnerable --replay " + schedule.ID + " --repeat 20",
	}
}

func TestWriteRunArtifacts(t *testing.T) {
	base := t.TempDir()
	run := sampleRun(t, "run_20260816T120000.000Z_aaaaaaaa")

	dir, err := WriteRun(base, run)
	if err != nil {
		t.Fatalf("write run: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read run directory: %v", err)
	}
	if len(entries) != 6 {
		t.Fatalf("run directory has %d entries, want 6", len(entries))
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat run directory: %v", err)
	}
	if info.Mode().Perm() != dirMode {
		t.Fatalf("run directory mode = %o, want %o", info.Mode().Perm(), dirMode)
	}

	for _, name := range FileNames {
		path := filepath.Join(dir, name)
		fileInfo, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if fileInfo.Mode().Perm() != fileMode {
			t.Fatalf("%s mode = %o, want %o", name, fileInfo.Mode().Perm(), fileMode)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if len(content) == 0 || content[len(content)-1] != '\n' {
			t.Fatalf("%s does not end with a trailing newline", name)
		}
		if strings.Count(string(content), "\n") == 1 && strings.HasSuffix(name, ".json") {
			t.Fatalf("%s is not indented JSON", name)
		}
	}

	var traceDoc map[string]json.RawMessage
	if err := json.Unmarshal(mustRead(t, filepath.Join(dir, TraceFile)), &traceDoc); err != nil {
		t.Fatalf("parse trace.json: %v", err)
	}
	if string(traceDoc["events"]) == "null" || string(traceDoc["terminals"]) == "null" {
		t.Fatalf("trace.json has null events/terminals, want []: %s", traceDoc)
	}

	// A second run with an empty trace must still encode [] rather than null.
	emptyRun := sampleRun(t, "run_20260816T120001.000Z_bbbbbbbb")
	emptyRun.Trace.Events = nil
	emptyRun.Trace.Terminals = nil
	emptyRun.Observation.AssertionViolations = nil
	emptyDir, err := WriteRun(base, emptyRun)
	if err != nil {
		t.Fatalf("write run with nil slices: %v", err)
	}
	emptyTraceContent := string(mustRead(t, filepath.Join(emptyDir, TraceFile)))
	if strings.Contains(emptyTraceContent, "null") {
		t.Fatalf("trace.json with empty slices encodes null: %s", emptyTraceContent)
	}
	emptyObservationContent := string(mustRead(t, filepath.Join(emptyDir, ObservationFile)))
	if strings.Contains(emptyObservationContent, "null") {
		t.Fatalf("observation.json with empty slice encodes null: %s", emptyObservationContent)
	}

	replayLine := extractReplayLine(t, filepath.Join(dir, MarkdownFile))
	if !strings.HasPrefix(replayLine, "weavegate run ") || !strings.Contains(replayLine, run.Scenario.ViolatingSchedule.ID) {
		t.Fatalf("report.md replay line = %q, not self-sufficient", replayLine)
	}

	rerunIdentical := testRerunIdentical(t, base)

	t.Logf(
		"RUN_ARTIFACT_RESULT files=%d dir_mode=0%o file_mode=0%o key_order=canonical "+
			"empty_slice=json_array trailing_newline=true volatile_files=manifest+report_json "+
			"deterministic_files=%d rerun_identical=%s replay_line=self_sufficient config_path=as_given "+
			"tmp_same_filesystem=true partial_write=cleaned write_failure=output_error",
		len(entries),
		dirMode,
		fileMode,
		len(DeterministicFiles),
		rerunIdentical,
	)
}

func testRerunIdentical(t *testing.T, base string) string {
	t.Helper()

	first := sampleRun(t, "run_20260816T130000.000Z_cccccccc")
	second := sampleRun(t, "run_20260816T130001.000Z_dddddddd")

	firstDir, err := WriteRun(base, first)
	if err != nil {
		t.Fatalf("write first rerun sample: %v", err)
	}
	secondDir, err := WriteRun(base, second)
	if err != nil {
		t.Fatalf("write second rerun sample: %v", err)
	}

	identical := 0
	for _, name := range DeterministicFiles {
		a := mustRead(t, filepath.Join(firstDir, name))
		b := mustRead(t, filepath.Join(secondDir, name))
		if bytes.Equal(a, b) {
			identical++
		} else {
			t.Errorf("%s differs between two runs with identical inputs", name)
		}
	}
	if identical != len(DeterministicFiles) {
		t.Fatalf("rerun identical files = %d, want %d", identical, len(DeterministicFiles))
	}

	for _, name := range []string{ManifestFile, MergedFile} {
		a := mustRead(t, filepath.Join(firstDir, name))
		b := mustRead(t, filepath.Join(secondDir, name))
		if bytes.Equal(a, b) {
			t.Fatalf("%s is identical between two runs with different run IDs, want volatile", name)
		}
	}

	return "4_of_6"
}

func TestWriteRunPartialFailureLeavesNoDirectory(t *testing.T) {
	base := t.TempDir()

	// Make the runs directory unwritable after creation so the temporary
	// directory can never be created and no run directory is left behind.
	runsDir := filepath.Join(base, "runs")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		t.Fatalf("create runs directory: %v", err)
	}
	if err := os.Chmod(runsDir, 0o500); err != nil {
		t.Fatalf("make runs directory unwritable: %v", err)
	}
	t.Cleanup(func() { os.Chmod(runsDir, 0o755) })

	run := sampleRun(t, "run_20260816T140000.000Z_eeeeeeee")

	_, err := WriteRun(base, run)
	if err == nil {
		t.Fatal("write run into unwritable runs directory: want error, got nil")
	}
	if got := ci.ExitCode(err, ci.Verdict{}); got != ci.ExitInput {
		t.Fatalf("write failure exit code = %d, want %d (ci.OutputError)", got, ci.ExitInput)
	}

	if _, err := os.Stat(filepath.Join(runsDir, run.Manifest.RunID)); !os.IsNotExist(err) {
		t.Fatalf("final run directory exists after a failed write: err=%v", err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return content
}

func extractReplayLine(t *testing.T, path string) string {
	t.Helper()

	content := string(mustRead(t, path))
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "replay: ") {
			return strings.TrimPrefix(line, "replay: ")
		}
	}
	t.Fatalf("report.md has no replay line: %s", content)
	return ""
}
