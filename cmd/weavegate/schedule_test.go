package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/weavegate/weavegate/internal/scenario"
)

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
	runDir := filepath.Join(literalOut, "runs", "run_saved")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("create literal output run: %v", err)
	}
	saved := plan.ReplaySchedule.Clone()
	writeV2ScenarioDoc(t, filepath.Join(runDir, "scenario.json"), saved)
	resolved, err := resolveReplaySchedule(saved.ID, literalOut, nil)
	if err != nil {
		t.Fatalf("resolve saved schedule under literal output path: %v", err)
	}
	if resolved.ID != saved.ID {
		t.Fatalf("literal output schedule ID = %q, want %q", resolved.ID, saved.ID)
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

	t.Log("CLI_REPLAY_LOOKUP_RESULT embedded=true outside_repo=true literal_out=true id_grammar=strict reader=v1+v2")
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
