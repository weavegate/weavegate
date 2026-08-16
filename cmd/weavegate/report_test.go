package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weavegate/weavegate/internal/ci"
	"github.com/weavegate/weavegate/internal/report"
)

func sampleReportRunAt(t *testing.T, runID string, startedAt time.Time) report.Run {
	t.Helper()

	schedule := report.Schedule{ID: "sch_sample00000", Steps: []report.CoordinationStep{{Worker: "w1", Point: "p1"}}}
	return report.Run{
		Manifest: report.Manifest{
			RunID:            runID,
			StartedAt:        startedAt,
			WeavegateVersion: "0.0.0-dev",
		},
		Scenario: report.Scenario{
			Name:       "concurrent-assign",
			Workers:    []report.Worker{{ID: "w1", Command: "assign"}},
			SyncPoints: []string{"p1"},
			Schedule:   &schedule,
		},
		Observation: report.Observation{
			Mode: "replay",
			AssertionViolations: []report.AssertionViolation{
				{OracleID: "active-assignment-is-unique", Rows: []report.Row{{"active_count": int64(2)}}},
			},
			Repeat:        20,
			ViolationRuns: 20,
		},
		Pass: false,
		ReplayCommand: "weavegate run --config config.yaml --scenario concurrent-assign " +
			"--variant vulnerable --out .weavegate --replay " + schedule.ID + " --repeat 20",
	}
}

func TestReportCommand(t *testing.T) {
	observed := map[string]string{}
	outDir := t.TempDir()

	olderID := "run_20260103T000000.000000001Z_ffffffffffffffffffffffffffffffff"
	newerID := "run_20260101T000000.000000001Z_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tieID := "run_20260102T000000.000000001Z_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	olderAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newerAt := olderAt.Add(time.Hour)

	if _, err := report.WriteRun(outDir, sampleReportRunAt(t, olderID, olderAt)); err != nil {
		t.Fatalf("write older run: %v", err)
	}
	if _, err := report.WriteRun(outDir, sampleReportRunAt(t, newerID, newerAt)); err != nil {
		t.Fatalf("write newer run: %v", err)
	}
	if _, err := report.WriteRun(outDir, sampleReportRunAt(t, tieID, newerAt)); err != nil {
		t.Fatalf("write tied run: %v", err)
	}
	writeIgnoredRunDirectories(t, outDir, newerAt.Add(time.Hour))

	generatedID, err := newRunID(newerAt)
	if err != nil || !validRunID(generatedID) {
		t.Fatalf("generated run ID = %q, %v", generatedID, err)
	}
	observed["run_identity"] = "opaque_128bit"

	t.Run("json_byte_identical", func(t *testing.T) {
		stdout, stderr, exit := run("report", newerID, "--format", "json", "--out", outDir)
		if exit != ci.ExitOK {
			t.Fatalf("report json exit = %d, want %d; stderr=%s", exit, ci.ExitOK, stderr)
		}
		want := mustReadReportFile(t, outDir, newerID, report.MergedFile)
		if stdout != want {
			t.Fatalf("report json output differs from the stored file")
		}
		observed["json"] = "byte_identical"
	})

	t.Run("md_byte_identical", func(t *testing.T) {
		stdout, stderr, exit := run("report", newerID, "--format", "md", "--out", outDir)
		if exit != ci.ExitOK {
			t.Fatalf("report md exit = %d, want %d; stderr=%s", exit, ci.ExitOK, stderr)
		}
		want := mustReadReportFile(t, outDir, newerID, report.MarkdownFile)
		if stdout != want {
			t.Fatalf("report md output differs from the stored file")
		}
		observed["md"] = "byte_identical"
		observed["no_rerender"] = "true"
	})

	t.Run("default_format", func(t *testing.T) {
		stdout, stderr, exit := run("report", newerID, "--out", outDir)
		if exit != ci.ExitOK {
			t.Fatalf("report default-format exit = %d, want %d; stderr=%s", exit, ci.ExitOK, stderr)
		}
		want := mustReadReportFile(t, outDir, newerID, report.MergedFile)
		if stdout != want {
			t.Fatalf("default-format report output != report.json content")
		}
		observed["default_format"] = "json"
	})

	t.Run("latest_run", func(t *testing.T) {
		stdout, stderr, exit := run("report", "--out", outDir)
		if exit != ci.ExitOK {
			t.Fatalf("report latest-run exit = %d, want %d; stderr=%s", exit, ci.ExitOK, stderr)
		}
		want := mustReadReportFile(t, outDir, tieID, report.MergedFile)
		if stdout != want {
			t.Fatalf("omitting the run ID did not resolve by manifest timestamp and ID tie-break")
		}
		observed["latest_run"] = "manifest_started_at"
		observed["same_time_ties"] = "id_tiebreak"
		observed["invalid_dirs"] = "ignored"
	})

	t.Run("missing_run", func(t *testing.T) {
		_, _, exit := run("report", "run_20260104T000000.000000001Z_cccccccccccccccccccccccccccccccc", "--out", outDir)
		if exit != ci.ExitInput {
			t.Fatalf("missing run exit = %d, want %d", exit, ci.ExitInput)
		}
		observed["missing_run"] = "5"
	})

	t.Run("invalid_run_id", func(t *testing.T) {
		_, _, exit := run("report", "../report.json", "--out", outDir)
		if exit != ci.ExitInput {
			t.Fatalf("path-shaped run ID exit = %d, want %d", exit, ci.ExitInput)
		}
		observed["invalid_run_id"] = "5"
	})

	t.Run("bad_format", func(t *testing.T) {
		_, _, exit := run("report", newerID, "--format", "xml", "--out", outDir)
		if exit != ci.ExitInput {
			t.Fatalf("bad format exit = %d, want %d", exit, ci.ExitInput)
		}
		observed["bad_format"] = "5"
	})

	order := []string{
		"json", "md", "default_format", "run_identity", "latest_run",
		"same_time_ties", "invalid_dirs", "missing_run", "invalid_run_id", "bad_format", "no_rerender",
	}
	parts := make([]string, 0, len(order))
	for _, key := range order {
		value, ok := observed[key]
		if !ok {
			t.Fatalf("marker attribute %q was not observed by any subtest", key)
		}
		parts = append(parts, key+"="+value)
	}
	t.Logf("CLI_REPORT_RESULT %s", strings.Join(parts, " "))
}

func writeIgnoredRunDirectories(t *testing.T, outDir string, future time.Time) {
	t.Helper()
	runsDir := filepath.Join(outDir, "runs")
	for _, name := range []string{
		".tmp-unpublished",
		"run_malformed",
		"run_20260105T000000.000000001Z_dddddddddddddddddddddddddddddddd",
	} {
		if err := os.Mkdir(filepath.Join(runsDir, name), 0o755); err != nil {
			t.Fatalf("create ignored run directory %q: %v", name, err)
		}
	}

	mismatchID := "run_20260106T000000.000000001Z_eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	if _, err := report.WriteRun(outDir, sampleReportRunAt(t, mismatchID, future)); err != nil {
		t.Fatalf("write mismatched run: %v", err)
	}
	manifestPath := filepath.Join(runsDir, mismatchID, report.ManifestFile)
	content, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read mismatched manifest: %v", err)
	}
	var manifest report.Manifest
	if err := json.Unmarshal(content, &manifest); err != nil {
		t.Fatalf("parse mismatched manifest: %v", err)
	}
	manifest.RunID = "run_20260107T000000.000000001Z_11111111111111111111111111111111"
	content, err = json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("marshal mismatched manifest: %v", err)
	}
	content = append(content, '\n')
	if err := os.WriteFile(manifestPath, content, 0o644); err != nil {
		t.Fatalf("write mismatched manifest: %v", err)
	}
}

func mustReadReportFile(t *testing.T, outDir, runID, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(outDir, "runs", runID, name))
	if err != nil {
		t.Fatalf("read expected %s: %v", name, err)
	}
	return string(content)
}
