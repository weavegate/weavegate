package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/weavegate/weavegate/internal/ci"
	"github.com/weavegate/weavegate/internal/scenario"
)

// resolveReplaySchedule interprets a --replay value (A-5). A value
// containing "/" or ".json" is a file path loaded directly. Otherwise it is
// a schedule ID, resolved in order from ① this --out directory's own run
// evidence, then ② the entrypoint's registered schedules directory. Both
// stages sort candidate paths for a deterministic search order. Several
// candidates sharing the ID are accepted only when their steps agree;
// otherwise the ID is ambiguous and rejected rather than guessed at.
func resolveReplaySchedule(value, outDir, schedulesDir string) (scenario.Schedule, error) {
	if strings.Contains(value, "/") || strings.Contains(value, ".json") {
		schedule, err := scenario.LoadScheduleFile(value)
		if err != nil {
			return scenario.Schedule{}, ci.InputError(fmt.Errorf("resolve replay schedule file %q: %w", value, err))
		}
		return schedule, nil
	}

	candidates, err := findScheduleByID(filepath.Join(outDir, "runs", "*", "scenario.json"), value, extractRunDirectorySchedule)
	if err != nil {
		return scenario.Schedule{}, err
	}
	if len(candidates) == 0 && strings.TrimSpace(schedulesDir) != "" {
		candidates, err = findScheduleByID(filepath.Join(schedulesDir, "*.json"), value, extractRegistrySchedule)
		if err != nil {
			return scenario.Schedule{}, err
		}
	}
	if len(candidates) == 0 {
		return scenario.Schedule{}, ci.InputError(fmt.Errorf(
			"resolve replay schedule %q: not found in %q or %q",
			value,
			outDir,
			schedulesDir,
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

func findScheduleByID(
	pattern, id string,
	extract func([]byte) (*scenario.Schedule, error),
) ([]scenario.Schedule, error) {
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return nil, ci.InputError(fmt.Errorf("search %q: %w", pattern, err))
	}
	sort.Strings(paths)

	var matches []scenario.Schedule
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		schedule, err := extract(content)
		if err != nil || schedule == nil {
			continue
		}
		if schedule.ID == id {
			matches = append(matches, *schedule)
		}
	}
	return matches, nil
}

type scenarioFileShape struct {
	ViolatingSchedule *scenario.Schedule `json:"violating_schedule"`
}

func extractRunDirectorySchedule(content []byte) (*scenario.Schedule, error) {
	var doc scenarioFileShape
	if err := json.Unmarshal(content, &doc); err != nil {
		return nil, err
	}
	return doc.ViolatingSchedule, nil
}

func extractRegistrySchedule(content []byte) (*scenario.Schedule, error) {
	var schedule scenario.Schedule
	if err := json.Unmarshal(content, &schedule); err != nil {
		return nil, err
	}
	return &schedule, nil
}
