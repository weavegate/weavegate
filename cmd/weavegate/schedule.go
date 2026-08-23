package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"github.com/weavegate/weavegate/internal/ci"
	"github.com/weavegate/weavegate/internal/scenario"
)

var scheduleIDPattern = regexp.MustCompile(`^sch_[0-9a-f]{12}$`)

// resolveReplaySchedule interprets a --replay value (A-5). Exactly
// "sch_" plus 12 lowercase hexadecimal characters is a schedule ID; every
// other non-empty value is a file path. IDs resolve in order from ① this
// --out directory's own run evidence, ② portable files under
// <out>/schedules, then ③ the entrypoint's embedded schedules. Several
// candidates sharing the ID are accepted only when their steps agree;
// otherwise the ID is ambiguous and rejected rather than guessed at.
func resolveReplaySchedule(value, outDir string, schedules fs.FS) (scenario.Schedule, error) {
	if !scheduleIDPattern.MatchString(value) {
		schedule, err := scenario.LoadScheduleFile(value)
		if err != nil {
			return scenario.Schedule{}, ci.InputError(fmt.Errorf("resolve replay schedule file %q: %w", value, err))
		}
		return schedule, nil
	}

	candidates, err := findSavedSchedulesByID(outDir, value)
	if err != nil {
		return scenario.Schedule{}, err
	}
	if len(candidates) == 0 {
		candidates, err = findSchedulesDirectoryByID(outDir, value)
		if err != nil {
			return scenario.Schedule{}, err
		}
	}
	if len(candidates) == 0 && schedules != nil {
		candidates, err = findEmbeddedSchedulesByID(schedules, value)
		if err != nil {
			return scenario.Schedule{}, err
		}
	}
	if len(candidates) == 0 {
		return scenario.Schedule{}, ci.InputError(fmt.Errorf(
			"resolve replay schedule %q: not found in %q, %q, or %q",
			value,
			outDir,
			filepath.Join(outDir, "schedules"),
			"embedded schedules",
		))
	}

	baseline := candidates[0]
	for _, candidate := range candidates[1:] {
		if !slices.Equal(candidate.Steps, baseline.Steps) {
			return scenario.Schedule{}, ci.InputError(fmt.Errorf(
				"resolve replay schedule %q: ambiguous_schedule: conflicting saved content across candidates",
				value,
			))
		}
	}
	return baseline, nil
}

func findSchedulesDirectoryByID(outDir, id string) ([]scenario.Schedule, error) {
	schedulesDir := filepath.Join(outDir, "schedules")
	entries, err := os.ReadDir(schedulesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, ci.InputError(fmt.Errorf("search portable schedules in %q: %w", schedulesDir, err))
	}

	var matches []scenario.Schedule
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(schedulesDir, entry.Name())
		scheduleValue, err := scenario.LoadScheduleFile(path)
		if err != nil {
			return nil, ci.InputError(fmt.Errorf("parse portable schedule %q: %w", path, err))
		}
		if scheduleValue.ID == id {
			matches = append(matches, scheduleValue)
		}
	}
	return matches, nil
}

func findSavedSchedulesByID(outDir, id string) ([]scenario.Schedule, error) {
	runsDir := filepath.Join(outDir, "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, ci.InputError(fmt.Errorf("search saved schedules in %q: %w", runsDir, err))
	}

	var matches []scenario.Schedule
	for _, entry := range entries {
		if !entry.IsDir() || !validRunID(entry.Name()) {
			continue
		}
		path := filepath.Join(runsDir, entry.Name(), "scenario.json")
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		schedule, err := extractRunDirectorySchedule(content)
		if err != nil || schedule == nil {
			continue
		}
		contentID, err := scenario.ContentID(schedule.Steps)
		if err != nil || contentID != schedule.ID {
			continue
		}
		if schedule.ID == id {
			matches = append(matches, *schedule)
		}
	}
	return matches, nil
}

func findEmbeddedSchedulesByID(schedules fs.FS, id string) ([]scenario.Schedule, error) {
	entries, err := fs.ReadDir(schedules, ".")
	if err != nil {
		return nil, ci.InputError(fmt.Errorf("search embedded schedules: %w", err))
	}
	var matches []scenario.Schedule
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		content, err := fs.ReadFile(schedules, entry.Name())
		if err != nil {
			return nil, ci.InputError(fmt.Errorf("read embedded schedule %q: %w", entry.Name(), err))
		}
		schedule, err := extractRegistrySchedule(content)
		if err != nil {
			return nil, ci.InputError(fmt.Errorf("parse embedded schedule %q: %w", entry.Name(), err))
		}
		if schedule.ID == id {
			matches = append(matches, *schedule)
		}
	}
	return matches, nil
}

type scenarioFileShape struct {
	Schedule          *scenario.Schedule `json:"schedule"`
	ViolatingSchedule *scenario.Schedule `json:"violating_schedule"`
}

func extractRunDirectorySchedule(content []byte) (*scenario.Schedule, error) {
	var doc scenarioFileShape
	if err := json.Unmarshal(content, &doc); err != nil {
		return nil, err
	}
	if doc.Schedule != nil {
		return doc.Schedule, nil
	}
	return doc.ViolatingSchedule, nil
}

func extractRegistrySchedule(content []byte) (*scenario.Schedule, error) {
	schedule, err := scenario.LoadSchedule(bytes.NewReader(content))
	if err != nil {
		return nil, err
	}
	return &schedule, nil
}
