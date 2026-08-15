// Package scenario defines replayable worker scenarios and their saved
// coordination schedules.
package scenario

import (
	"errors"
	"fmt"
	"strings"

	"github.com/weavegate/weavegate/internal/sut"
)

// Scenario names the workers, sync-points, and SUT configuration used by a
// replay. SyncPoints is the required order for every worker.
type Scenario struct {
	Name       string
	Workers    []Worker
	SyncPoints []string
	SUTConfig  sut.SUTConfig
}

// Worker binds a stable worker ID to an adapter command.
type Worker struct {
	ID      string
	Command string
}

type scenarioIndex struct {
	workers map[string]struct{}
	points  map[string]int
}

func validateScenario(scenario Scenario) (scenarioIndex, error) {
	if strings.TrimSpace(scenario.Name) == "" {
		return scenarioIndex{}, errors.New("validate scenario: name is required")
	}
	if len(scenario.Workers) == 0 {
		return scenarioIndex{}, fmt.Errorf("validate scenario %q: at least one worker is required", scenario.Name)
	}
	if len(scenario.SyncPoints) == 0 {
		return scenarioIndex{}, fmt.Errorf("validate scenario %q: at least one sync-point is required", scenario.Name)
	}

	index := scenarioIndex{
		workers: make(map[string]struct{}, len(scenario.Workers)),
		points:  make(map[string]int, len(scenario.SyncPoints)),
	}
	for workerIndex, worker := range scenario.Workers {
		if strings.TrimSpace(worker.ID) == "" {
			return scenarioIndex{}, fmt.Errorf(
				"validate scenario %q worker[%d]: ID is required",
				scenario.Name,
				workerIndex,
			)
		}
		if strings.TrimSpace(worker.Command) == "" {
			return scenarioIndex{}, fmt.Errorf(
				"validate scenario %q worker[%d] %q: command is required",
				scenario.Name,
				workerIndex,
				worker.ID,
			)
		}
		if _, exists := index.workers[worker.ID]; exists {
			return scenarioIndex{}, fmt.Errorf(
				"validate scenario %q worker[%d]: duplicate worker %q",
				scenario.Name,
				workerIndex,
				worker.ID,
			)
		}
		index.workers[worker.ID] = struct{}{}
	}

	for pointIndex, point := range scenario.SyncPoints {
		if strings.TrimSpace(point) == "" {
			return scenarioIndex{}, fmt.Errorf(
				"validate scenario %q sync-point[%d]: name is required",
				scenario.Name,
				pointIndex,
			)
		}
		if _, exists := index.points[point]; exists {
			return scenarioIndex{}, fmt.Errorf(
				"validate scenario %q sync-point[%d]: duplicate point %q",
				scenario.Name,
				pointIndex,
				point,
			)
		}
		index.points[point] = pointIndex
	}

	return index, nil
}

// Clone returns a deep copy that can be mutated independently of s.
func (s Scenario) Clone() Scenario {
	clone := s
	clone.Workers = append([]Worker(nil), s.Workers...)
	clone.SyncPoints = append([]string(nil), s.SyncPoints...)
	clone.SUTConfig.Params = cloneStringMap(s.SUTConfig.Params)
	return clone
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}

	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}
