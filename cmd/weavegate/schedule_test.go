package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weavegate/weavegate/internal/ci"
	"github.com/weavegate/weavegate/internal/scenario"
)

type unreadableSchedulesFS struct{}

func (unreadableSchedulesFS) Open(string) (fs.File, error) {
	return nil, errors.New("embedded schedules must not be read")
}

func TestReplayLookupIsEmbeddedAndLiteral(t *testing.T) {
	configPath := filepath.Join(repoRoot(t), "fixtures", "matching-slice", ".weavegate", "config.yaml")
	outsideRepo := t.TempDir()
	chdir(t, outsideRepo)

	plan, err := buildRunPlan(runFlags{
		config: configPath, scenario: "concurrent-assign",
		replay: "sch_ba00582f9632", replaySet: true, out: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("build embedded replay plan outside repository: %v", err)
	}
	if plan.ReplaySchedule == nil || plan.ReplaySchedule.ID != "sch_ba00582f9632" {
		t.Fatalf("embedded replay schedule = %+v", plan.ReplaySchedule)
	}

	literalOut := filepath.Join(t.TempDir(), "out[abc]*?")
	runID := "run_20260823T000000.000000001Z_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	runDir := filepath.Join(literalOut, "runs", runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("create literal output run: %v", err)
	}
	saved := plan.ReplaySchedule.Clone()
	writeV2ScenarioDoc(t, filepath.Join(runDir, "scenario.json"), saved)
	stagingDir := filepath.Join(literalOut, "runs", ".tmp-"+runID)
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		t.Fatalf("create staging output run: %v", err)
	}
	stagingSchedule := saved.Clone()
	stagingSchedule.Steps = append(stagingSchedule.Steps,
		scenario.CoordinationStep{Worker: "staging", Point: "must-be-ignored"})
	writeV2ScenarioDoc(t, filepath.Join(stagingDir, "scenario.json"), stagingSchedule)
	resolved, err := resolveReplaySchedule(saved.ID, literalOut, nil)
	if err != nil {
		t.Fatalf("resolve saved schedule under literal output path: %v", err)
	}
	if resolved.ID != saved.ID {
		t.Fatalf("literal output schedule ID = %q, want %q", resolved.ID, saved.ID)
	}
	malformedSchedulesDir := filepath.Join(literalOut, "schedules")
	if err := os.MkdirAll(malformedSchedulesDir, 0o755); err != nil {
		t.Fatalf("create later-stage schedules directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(malformedSchedulesDir, "malformed.json"), []byte("{"), 0o644); err != nil {
		t.Fatalf("write malformed later-stage schedule: %v", err)
	}
	resolved, err = resolveReplaySchedule(saved.ID, literalOut, unreadableSchedulesFS{})
	if err != nil || resolved.ID != saved.ID {
		t.Fatalf("run evidence did not take priority = %q, %v", resolved.ID, err)
	}

	portableOut := t.TempDir()
	portableDir := filepath.Join(portableOut, "schedules")
	if err := os.MkdirAll(portableDir, 0o755); err != nil {
		t.Fatalf("create portable schedules directory: %v", err)
	}
	if err := scenario.WriteScheduleFile(filepath.Join(portableDir, "schedule.json"), saved); err != nil {
		t.Fatalf("write portable schedule: %v", err)
	}
	resolved, err = resolveReplaySchedule(saved.ID, portableOut, unreadableSchedulesFS{})
	if err != nil || resolved.ID != saved.ID {
		t.Fatalf("resolve portable schedule before embedded = %q, %v", resolved.ID, err)
	}
	stagingOnlyOut := t.TempDir()
	stagingOnlyRunDir := filepath.Join(stagingOnlyOut, "runs", ".tmp-"+runID)
	if err := os.MkdirAll(stagingOnlyRunDir, 0o755); err != nil {
		t.Fatalf("create staging-only output run: %v", err)
	}
	writeV2ScenarioDoc(t, filepath.Join(stagingOnlyRunDir, "scenario.json"), saved)
	if err := os.MkdirAll(filepath.Join(stagingOnlyOut, "schedules"), 0o755); err != nil {
		t.Fatalf("create staging-only portable schedules directory: %v", err)
	}
	if err := scenario.WriteScheduleFile(filepath.Join(stagingOnlyOut, "schedules", "schedule.json"), saved); err != nil {
		t.Fatalf("write staging-only portable schedule: %v", err)
	}
	resolved, err = resolveReplaySchedule(saved.ID, stagingOnlyOut, unreadableSchedulesFS{})
	if err != nil || resolved.ID != saved.ID {
		t.Fatalf("staging-only evidence did not fall through = %q, %v", resolved.ID, err)
	}
	unverifiedOut := t.TempDir()
	unverifiedRunDir := filepath.Join(unverifiedOut, "runs", runID)
	if err := os.MkdirAll(unverifiedRunDir, 0o755); err != nil {
		t.Fatalf("create unverified output run: %v", err)
	}
	unverifiedSchedule := saved.Clone()
	unverifiedSchedule.Steps = append(unverifiedSchedule.Steps,
		scenario.CoordinationStep{Worker: "unverified", Point: "must-fall-through"})
	writeV2ScenarioDoc(t, filepath.Join(unverifiedRunDir, "scenario.json"), unverifiedSchedule)
	if err := os.MkdirAll(filepath.Join(unverifiedOut, "schedules"), 0o755); err != nil {
		t.Fatalf("create verified portable schedules directory: %v", err)
	}
	if err := scenario.WriteScheduleFile(filepath.Join(unverifiedOut, "schedules", "schedule.json"), saved); err != nil {
		t.Fatalf("write verified portable schedule: %v", err)
	}
	resolved, err = resolveReplaySchedule(saved.ID, unverifiedOut, unreadableSchedulesFS{})
	if err != nil || resolved.ID != saved.ID || len(resolved.Steps) != len(saved.Steps) {
		t.Fatalf("unverified run evidence did not fall through = %+v, %v", resolved, err)
	}

	malformedOut := t.TempDir()
	if err := os.MkdirAll(filepath.Join(malformedOut, "schedules"), 0o755); err != nil {
		t.Fatalf("create malformed schedules directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(malformedOut, "schedules", "bad.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write malformed schedule: %v", err)
	}
	if _, err := resolveReplaySchedule(saved.ID, malformedOut, nil); err == nil || ci.ExitCode(err, ci.Verdict{}) != ci.ExitInput {
		t.Fatalf("malformed schedules file error = %v, want input error", err)
	}

	unresolvedOut := t.TempDir()
	_, err = resolveReplaySchedule("sch_000000000000", unresolvedOut, nil)
	if err == nil {
		t.Fatal("resolve missing schedule: want error")
	}
	for _, location := range []string{unresolvedOut, filepath.Join(unresolvedOut, "schedules"), "embedded schedules"} {
		if !strings.Contains(err.Error(), location) {
			t.Fatalf("unresolved error %q does not name %q", err, location)
		}
	}

	pathWithoutExtension := filepath.Join(t.TempDir(), "custom-schedule")
	var encoded bytes.Buffer
	if err := scenario.WriteSchedule(&encoded, saved); err != nil {
		t.Fatalf("encode custom schedule: %v", err)
	}
	if err := os.WriteFile(pathWithoutExtension, encoded.Bytes(), 0o644); err != nil {
		t.Fatalf("write extensionless schedule: %v", err)
	}
	resolved, err = resolveReplaySchedule(pathWithoutExtension, t.TempDir(), nil)
	if err != nil || resolved.ID != saved.ID {
		t.Fatalf("resolve extensionless schedule = %q, %v", resolved.ID, err)
	}

	t.Log(`CLI_REPLAY_LOOKUP_RESULT embedded=true schedules_dir=true stage_order=run_evidence,schedules_dir,embedded outside_repo=true literal_out=true id_grammar=strict run_dir_grammar=enforced staging_dir=skipped run_evidence_id=verified unverified_run_evidence=falls_through malformed_schedules_file=error unresolved_names_all_stages=true reader=v1+v2
`)
}

func TestScenarioScheduleReaderAcceptsV2AndLegacyV1(t *testing.T) {
	scheduleValue, err := scenario.NewSchedule([]scenario.CoordinationStep{{Worker: "w1", Point: "p1"}})
	if err != nil {
		t.Fatalf("build schedule: %v", err)
	}
	v2, err := json.Marshal(map[string]any{"schedule": scheduleValue})
	if err != nil {
		t.Fatalf("marshal v2 scenario: %v", err)
	}
	v1, err := json.Marshal(map[string]any{"violating_schedule": scheduleValue})
	if err != nil {
		t.Fatalf("marshal v1 scenario: %v", err)
	}

	for name, content := range map[string][]byte{"v2": v2, "v1": v1} {
		t.Run(name, func(t *testing.T) {
			got, err := extractRunDirectorySchedule(content)
			if err != nil || got == nil || got.ID != scheduleValue.ID {
				t.Fatalf("extract %s schedule = %+v, %v", name, got, err)
			}

			outDir := t.TempDir()
			runDir := filepath.Join(outDir, "runs", "run_20260823T000000.000000001Z_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
			if err := os.MkdirAll(runDir, 0o755); err != nil {
				t.Fatalf("create %s run directory: %v", name, err)
			}
			if err := os.WriteFile(filepath.Join(runDir, "scenario.json"), content, 0o644); err != nil {
				t.Fatalf("write %s scenario: %v", name, err)
			}
			resolved, err := resolveReplaySchedule(scheduleValue.ID, outDir, unreadableSchedulesFS{})
			if err != nil || resolved.ID != scheduleValue.ID {
				t.Fatalf("resolve %s run evidence = %+v, %v", name, resolved, err)
			}
		})
	}
}

func writeV2ScenarioDoc(t *testing.T, path string, scheduleValue scenario.Schedule) {
	t.Helper()
	content, err := json.Marshal(map[string]any{"schedule": scheduleValue})
	if err != nil {
		t.Fatalf("marshal v2 scenario: %v", err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write v2 scenario %q: %v", path, err)
	}
}
