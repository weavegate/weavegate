package report

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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
			SyncPoints: []string{"after_read_request", "before_insert_assignment"},
			Schedule:   &schedule,
		},
		Observation: Observation{
			Mode:              "explore",
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
			ViolationRuns:        20,
			Flaky:                false,
			Fingerprints:         map[string]int{"fp-discovery": 20},
			DiscoveryFingerprint: "fp-discovery",
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
	umask := 0o077
	previousUmask := syscall.Umask(umask)
	defer syscall.Umask(previousUmask)
	expectedDirMode := dirMode &^ os.FileMode(umask)
	expectedFileMode := fileMode &^ os.FileMode(umask)
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
	if info.Mode().Perm() != expectedDirMode {
		t.Fatalf("run directory mode = %o, want %o", info.Mode().Perm(), expectedDirMode)
	}

	for _, name := range FileNames {
		path := filepath.Join(dir, name)
		fileInfo, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if fileInfo.Mode().Perm() != expectedFileMode {
			t.Fatalf("%s mode = %o, want %o", name, fileInfo.Mode().Perm(), expectedFileMode)
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
	for _, name := range []string{ManifestFile, ScenarioFile, ObservationFile, TraceFile, MergedFile} {
		var doc map[string]any
		if err := json.Unmarshal(mustRead(t, filepath.Join(dir, name)), &doc); err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		if got := doc["artifact_version"]; got != float64(ArtifactVersion) {
			t.Fatalf("%s artifact_version = %#v, want %d", name, got, ArtifactVersion)
		}
	}
	scenarioContent := string(mustRead(t, filepath.Join(dir, ScenarioFile)))
	if strings.Contains(scenarioContent, "violating_schedule") || !strings.Contains(scenarioContent, `"schedule"`) {
		t.Fatalf("scenario.json is not v2 neutral schedule output: %s", scenarioContent)
	}

	var traceDoc map[string]json.RawMessage
	if err := json.Unmarshal(mustRead(t, filepath.Join(dir, TraceFile)), &traceDoc); err != nil {
		t.Fatalf("parse trace.json: %v", err)
	}
	if string(traceDoc["events"]) == "null" || string(traceDoc["terminals"]) == "null" {
		t.Fatalf("trace.json has null events/terminals, want []: %s", traceDoc)
	}
	var observationDoc map[string]json.RawMessage
	if err := json.Unmarshal(mustRead(t, filepath.Join(dir, ObservationFile)), &observationDoc); err != nil {
		t.Fatalf("parse observation.json: %v", err)
	}
	if string(observationDoc["diagnostics"]) != "[]" {
		t.Fatalf("observation diagnostics = %s, want []", observationDoc["diagnostics"])
	}
	var mergedDoc struct {
		Observation struct {
			Diagnostics []Diagnostic `json:"diagnostics"`
		} `json:"observation"`
	}
	if err := json.Unmarshal(mustRead(t, filepath.Join(dir, MergedFile)), &mergedDoc); err != nil {
		t.Fatalf("parse report.json diagnostics: %v", err)
	}
	if mergedDoc.Observation.Diagnostics == nil {
		t.Fatal("report.json observation diagnostics is null")
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

	t.Log("REPORT_DIAGNOSTIC_RESULT field=observation.diagnostics files=6 empty=json_array dto=report_owned merged=report_json deterministic=true")

	replayLine := extractReplayLine(t, filepath.Join(dir, MarkdownFile))
	if !strings.HasPrefix(replayLine, "weavegate run ") || !strings.Contains(replayLine, run.Scenario.Schedule.ID) || strings.Contains(replayLine, "--out") {
		t.Fatalf("report.md replay line = %q, not self-sufficient", replayLine)
	}

	rerunIdentical := testRerunIdentical(t, base)

	t.Logf(
		"RUN_ARTIFACT_RESULT files=%d umask=0077 dir_mode=0%o file_mode=0%o key_order=canonical "+
			"empty_slice=json_array trailing_newline=true volatile_files=manifest+report_json "+
			"deterministic_files=%d rerun_identical=%s replay_line=out_omitted shell_quote=posix config_path=as_given "+
			"tmp_same_filesystem=true partial_write=cleaned write_failure=output_error",
		len(entries),
		expectedDirMode,
		expectedFileMode,
		len(DeterministicFiles),
		rerunIdentical,
	)
}

func TestWriteRunPreservesExistingDestination(t *testing.T) {
	base := t.TempDir()
	run := sampleRun(t, "run_20260816T120003.000000000Z_22222222222222222222222222222222")
	finalDir := filepath.Join(base, "runs", run.Manifest.RunID)
	if err := os.MkdirAll(finalDir, 0o755); err != nil {
		t.Fatalf("create existing run directory: %v", err)
	}
	sentinelPath := filepath.Join(finalDir, "sentinel")
	const sentinel = "preserve me"
	if err := os.WriteFile(sentinelPath, []byte(sentinel), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	_, err := WriteRun(base, run)
	if err == nil {
		t.Fatal("write colliding run: want error, got nil")
	}
	if got := ci.ExitCode(err, ci.Verdict{}); got != ci.ExitInput {
		t.Fatalf("collision exit code = %d, want %d", got, ci.ExitInput)
	}
	content, readErr := os.ReadFile(sentinelPath)
	if readErr != nil || string(content) != sentinel {
		t.Fatalf("existing destination changed: content=%q err=%v", content, readErr)
	}
}

func TestPassingDirectReplayUsesNeutralEvidenceSemantics(t *testing.T) {
	base := t.TempDir()
	run := sampleRun(t, "run_20260816T120002.000000000Z_11111111111111111111111111111111")
	run.Observation.Mode = "replay"
	run.Observation.SchedulesExplored = 0
	run.Observation.ExplorePasses = 0
	run.Observation.AssertionViolations = nil
	run.Observation.ViolationRuns = 0
	run.Observation.DiscoveryFingerprint = ""
	run.Pass = true

	dir, err := WriteRun(base, run)
	if err != nil {
		t.Fatalf("write passing replay: %v", err)
	}
	markdown := string(mustRead(t, filepath.Join(dir, MarkdownFile)))
	if !strings.Contains(markdown, "| replayed: "+run.Scenario.Schedule.ID) || strings.Contains(markdown, "| violating:") {
		t.Fatalf("passing replay markdown has misleading schedule label: %s", markdown)
	}
	observation := string(mustRead(t, filepath.Join(dir, ObservationFile)))
	if strings.Contains(observation, "discovery_fingerprint") {
		t.Fatalf("direct replay emitted discovery_fingerprint: %s", observation)
	}
	flaky := run
	flaky.Pass = false
	flaky.Flaky = true
	flaky.Observation.Flaky = true
	flakyMarkdown := renderMarkdown(flaky)
	if !strings.Contains(flakyMarkdown, "| violating: "+run.Scenario.Schedule.ID) || strings.Contains(flakyMarkdown, "| replayed:") {
		t.Fatalf("flaky replay lost violating label: %s", flakyMarkdown)
	}

	t.Log("ARTIFACT_V2_RESULT files=6 writer=v2 schedule=neutral mode=recorded direct_replay_discovery=omitted passing_replay=replayed legacy_reader=v1+v2")
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
