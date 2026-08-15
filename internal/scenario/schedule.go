package scenario

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const scheduleIDPrefixLength = 12

// Schedule is a content-addressed total order of coordination intents.
type Schedule struct {
	ID    string             `json:"id"`
	Steps []CoordinationStep `json:"steps"`
}

// CoordinationStep identifies one worker arrival that a replay intends to
// release. This saved order is independent from the realized release order.
type CoordinationStep struct {
	Worker string `json:"worker"`
	Point  string `json:"point"`
}

// NewSchedule copies steps and assigns their canonical content-derived ID.
func NewSchedule(steps []CoordinationStep) (Schedule, error) {
	cloned := append([]CoordinationStep(nil), steps...)
	id, err := ContentID(cloned)
	if err != nil {
		return Schedule{}, err
	}
	return Schedule{ID: id, Steps: cloned}, nil
}

// Clone returns a deep copy that can be mutated independently of s.
func (s Schedule) Clone() Schedule {
	return Schedule{
		ID:    s.ID,
		Steps: append([]CoordinationStep(nil), s.Steps...),
	}
}

// ContentID derives a schedule ID from compact JSON containing only steps.
func ContentID(steps []CoordinationStep) (string, error) {
	canonical, err := json.Marshal(steps)
	if err != nil {
		return "", fmt.Errorf("marshal canonical schedule steps: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return fmt.Sprintf("sch_%x", sum[:scheduleIDPrefixLength/2]), nil
}

// LoadSchedule strictly decodes one saved schedule and verifies its content ID.
func LoadSchedule(reader io.Reader) (Schedule, error) {
	if reader == nil {
		return Schedule{}, errors.New("load schedule: reader is required")
	}

	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()

	var schedule Schedule
	if err := decoder.Decode(&schedule); err != nil {
		return Schedule{}, fmt.Errorf("load schedule JSON: %w", err)
	}

	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Schedule{}, errors.New("load schedule JSON: trailing JSON value")
		}
		return Schedule{}, fmt.Errorf("load schedule JSON trailing data: %w", err)
	}

	expectedID, err := ContentID(schedule.Steps)
	if err != nil {
		return Schedule{}, fmt.Errorf("load schedule %q: %w", schedule.ID, err)
	}
	if schedule.ID != expectedID {
		return Schedule{}, fmt.Errorf(
			"load schedule %q: content ID mismatch: expected %q",
			schedule.ID,
			expectedID,
		)
	}

	return schedule.Clone(), nil
}

// LoadScheduleFile loads and verifies a saved schedule artifact.
func LoadScheduleFile(path string) (Schedule, error) {
	file, err := os.Open(path)
	if err != nil {
		return Schedule{}, fmt.Errorf("load schedule file %q: %w", path, err)
	}
	defer file.Close()

	schedule, err := LoadSchedule(file)
	if err != nil {
		return Schedule{}, fmt.Errorf("load schedule file %q: %w", path, err)
	}
	return schedule, nil
}

// WriteSchedule writes one schedule using the canonical artifact format.
func WriteSchedule(writer io.Writer, schedule Schedule) error {
	if writer == nil {
		return errors.New("write schedule: writer is required")
	}
	if strings.TrimSpace(schedule.ID) == "" {
		return errors.New("write schedule: ID is required")
	}

	expectedID, err := ContentID(schedule.Steps)
	if err != nil {
		return fmt.Errorf("write schedule %q: %w", schedule.ID, err)
	}
	if schedule.ID != expectedID {
		return fmt.Errorf(
			"write schedule %q: content ID mismatch: expected %q",
			schedule.ID,
			expectedID,
		)
	}

	encoded, err := json.MarshalIndent(schedule, "", "  ")
	if err != nil {
		return fmt.Errorf("write schedule %q JSON: %w", schedule.ID, err)
	}
	encoded = append(encoded, '\n')

	written, err := writer.Write(encoded)
	if err != nil {
		return fmt.Errorf("write schedule %q: %w", schedule.ID, err)
	}
	if written != len(encoded) {
		return fmt.Errorf("write schedule %q: %w", schedule.ID, io.ErrShortWrite)
	}
	return nil
}

// WriteScheduleFile atomically replaces path with a canonical schedule file.
func WriteScheduleFile(path string, schedule Schedule) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("write schedule file: path is required")
	}

	var encoded bytes.Buffer
	if err := WriteSchedule(&encoded, schedule); err != nil {
		return fmt.Errorf("write schedule file %q: %w", path, err)
	}

	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return fmt.Errorf("write schedule file %q: create temporary file: %w", path, err)
	}
	temporaryPath := temporary.Name()
	keepTemporary := true
	defer func() {
		if keepTemporary {
			temporary.Close()
			os.Remove(temporaryPath)
		}
	}()

	written, err := temporary.Write(encoded.Bytes())
	if err != nil {
		return fmt.Errorf("write schedule file %q: write temporary file: %w", path, err)
	}
	if written != encoded.Len() {
		return fmt.Errorf("write schedule file %q: write temporary file: %w", path, io.ErrShortWrite)
	}
	if err := temporary.Chmod(0o644); err != nil {
		return fmt.Errorf("write schedule file %q: set temporary file mode: %w", path, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("write schedule file %q: close temporary file: %w", path, err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("write schedule file %q: replace destination: %w", path, err)
	}

	keepTemporary = false
	return nil
}

// Validate verifies the scenario value and that schedule contains every
// worker-point pair exactly once while preserving each worker's point order.
func Validate(scenario Scenario, schedule Schedule) error {
	scenarioIndex, err := validateScenario(scenario)
	if err != nil {
		return err
	}

	if strings.TrimSpace(schedule.ID) == "" {
		return fmt.Errorf("validate scenario %q schedule: ID is required", scenario.Name)
	}
	expectedID, err := ContentID(schedule.Steps)
	if err != nil {
		return fmt.Errorf("validate scenario %q schedule %q: %w", scenario.Name, schedule.ID, err)
	}
	if schedule.ID != expectedID {
		return fmt.Errorf(
			"validate scenario %q schedule %q: content ID mismatch: expected %q",
			scenario.Name,
			schedule.ID,
			expectedID,
		)
	}

	expectedSteps := len(scenario.Workers) * len(scenario.SyncPoints)
	seenPairs := make(map[CoordinationStep]int, expectedSteps)
	nextPoint := make(map[string]int, len(scenario.Workers))
	for index, step := range schedule.Steps {
		if strings.TrimSpace(step.Worker) == "" {
			return fmt.Errorf(
				"validate scenario %q schedule %q step[%d]: worker is required",
				scenario.Name,
				schedule.ID,
				index,
			)
		}
		if _, exists := scenarioIndex.workers[step.Worker]; !exists {
			return fmt.Errorf(
				"validate scenario %q schedule %q step[%d]: unknown worker %q",
				scenario.Name,
				schedule.ID,
				index,
				step.Worker,
			)
		}
		if strings.TrimSpace(step.Point) == "" {
			return fmt.Errorf(
				"validate scenario %q schedule %q step[%d] worker %q: point is required",
				scenario.Name,
				schedule.ID,
				index,
				step.Worker,
			)
		}
		pointIndex, exists := scenarioIndex.points[step.Point]
		if !exists {
			return fmt.Errorf(
				"validate scenario %q schedule %q step[%d] worker %q: unknown point %q",
				scenario.Name,
				schedule.ID,
				index,
				step.Worker,
				step.Point,
			)
		}
		if firstIndex, exists := seenPairs[step]; exists {
			return fmt.Errorf(
				"validate scenario %q schedule %q step[%d]: duplicate pair %q/%q (first at step[%d])",
				scenario.Name,
				schedule.ID,
				index,
				step.Worker,
				step.Point,
				firstIndex,
			)
		}
		if pointIndex != nextPoint[step.Worker] {
			expectedPoint := scenario.SyncPoints[nextPoint[step.Worker]]
			return fmt.Errorf(
				"validate scenario %q schedule %q step[%d] worker %q: point %q is out of order; expected %q",
				scenario.Name,
				schedule.ID,
				index,
				step.Worker,
				step.Point,
				expectedPoint,
			)
		}

		seenPairs[step] = index
		nextPoint[step.Worker]++
	}

	if len(schedule.Steps) != expectedSteps {
		for _, worker := range scenario.Workers {
			for _, point := range scenario.SyncPoints {
				pair := CoordinationStep{Worker: worker.ID, Point: point}
				if _, exists := seenPairs[pair]; !exists {
					return fmt.Errorf(
						"validate scenario %q schedule %q: missing pair %q/%q (got %d steps, want %d)",
						scenario.Name,
						schedule.ID,
						worker.ID,
						point,
						len(schedule.Steps),
						expectedSteps,
					)
				}
			}
		}
		return fmt.Errorf(
			"validate scenario %q schedule %q: got %d steps, want %d",
			scenario.Name,
			schedule.ID,
			len(schedule.Steps),
			expectedSteps,
		)
	}

	return nil
}
