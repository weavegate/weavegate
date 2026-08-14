// Package scenario defines replayable worker scenarios and their saved
// coordination schedules.
package scenario

import "github.com/weavegate/weavegate/internal/sut"

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
