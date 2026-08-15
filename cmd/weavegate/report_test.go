package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weavegate/weavegate/internal/ci"
	"github.com/weavegate/weavegate/internal/report"
	"github.com/weavegate/weavegate/internal/scenario"
)

func sampleReportRun(t *testing.T, runID string) report.Run {
	t.Helper()

	schedule, err := scenario.NewSchedule([]scenario.CoordinationStep{{Worker: "w1", Point: "p1"}})
	if err != nil {
		t.Fatalf("build sample schedule: %v", err)
	}
	return report.Run{
		Manifest: report.Manifest{
			RunID:            runID,
			StartedAt:        time.Now().UTC(),
			WeavegateVersion: "0.0.0-dev",
		},
		Scenario: report.Scenario{
			Name:              "concurrent-assign",
			Workers:           []scenario.Worker{{ID: "w1", Command: "assign"}},
			SyncPoints:        []string{"p1"},
			ViolatingSchedule: &schedule,
		},
		Observation: report.Observation{
			AssertionViolations: []string{"active-assignment-is-unique"},
			Repeat:              20,
			ViolationRuns:       20,
		},
		Pass: false,
		ReplayCommand: "weavegate run --config config.yaml --scenario concurrent-assign " +
			"--variant vulnerable --out .weavegate --replay " + schedule.ID + " --repeat 20",
	}
}

func TestReportCommand(t *testing.T) {
	observed := map[string]string{}
	outDir := t.TempDir()

	olderID := "run_20260101T000000.001Z_aaaaaaaa"
	newerID := "run_20260101T000000.002Z_bbbbbbbb"

	if _, err := report.WriteRun(outDir, sampleReportRun(t, olderID)); err != nil {
		t.Fatalf("write older run: %v", err)
	}
	if _, err := report.WriteRun(outDir, sampleReportRun(t, newerID)); err != nil {
		t.Fatalf("write newer run: %v", err)
	}

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
		want := mustReadReportFile(t, outDir, newerID, report.MergedFile)
		if stdout != want {
			t.Fatalf("omitting the run ID did not resolve to the lexicographically greatest (most recent) run")
		}
		observed["latest_run"] = "lexicographic_max"
		observed["same_second_ties"] = "ordered"
	})

	t.Run("missing_run", func(t *testing.T) {
		_, _, exit := run("report", "run_does_not_exist", "--out", outDir)
		if exit != ci.ExitInput {
			t.Fatalf("missing run exit = %d, want %d", exit, ci.ExitInput)
		}
		observed["missing_run"] = "5"
	})

	t.Run("bad_format", func(t *testing.T) {
		_, _, exit := run("report", newerID, "--format", "xml", "--out", outDir)
		if exit != ci.ExitInput {
			t.Fatalf("bad format exit = %d, want %d", exit, ci.ExitInput)
		}
		observed["bad_format"] = "5"
	})

	order := []string{
		"json", "md", "default_format", "latest_run",
		"same_second_ties", "missing_run", "bad_format", "no_rerender",
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

func mustReadReportFile(t *testing.T, outDir, runID, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(outDir, "runs", runID, name))
	if err != nil {
		t.Fatalf("read expected %s: %v", name, err)
	}
	return string(content)
}
