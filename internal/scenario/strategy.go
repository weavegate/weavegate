package scenario

import (
	"fmt"
	"iter"
	"math/big"
)

const defaultExhaustiveMaxCandidates = 5000

// SchedulePlan describes a strategy's candidate stream and whether its total
// size is known before the stream is consumed.
type SchedulePlan struct {
	Total      int                // Meaningful only when TotalKnown is true.
	TotalKnown bool               // False when a strategy cannot count in advance.
	Seq        iter.Seq[Schedule] // Candidate schedules in strategy order.
}

// Strategy produces saved coordination schedule candidates for a scenario.
type Strategy interface {
	Schedules(Scenario) (SchedulePlan, error)
}

// Exhaustive enumerates every saved coordination schedule that preserves each
// worker's sync-point order. A zero MaxCandidates uses the default limit of
// 5000.
type Exhaustive struct {
	MaxCandidates int
}

var _ Strategy = Exhaustive{}

// Schedules returns exhaustive candidates in lexicographic worker-index order.
func (strategy Exhaustive) Schedules(value Scenario) (SchedulePlan, error) {
	if strategy.MaxCandidates < 0 {
		return SchedulePlan{}, fmt.Errorf(
			"exhaustive schedules: max candidates must not be negative: %d",
			strategy.MaxCandidates,
		)
	}

	maxCandidates := strategy.MaxCandidates
	if maxCandidates == 0 {
		maxCandidates = defaultExhaustiveMaxCandidates
	}

	total, err := CandidateCount(value, maxCandidates)
	if err != nil {
		return SchedulePlan{}, fmt.Errorf("exhaustive schedules: %w", err)
	}

	value = value.Clone()
	return SchedulePlan{
		Total:      total,
		TotalKnown: true,
		Seq:        exhaustiveSequence(value),
	}, nil
}

// CandidateCount returns the number of per-worker-order-preserving schedules.
// It stops and returns an error as soon as the partial count exceeds max.
func CandidateCount(value Scenario, max int) (int, error) {
	if _, err := validateScenario(value); err != nil {
		return 0, err
	}
	if max <= 0 {
		return 0, fmt.Errorf("count schedule candidates for scenario %q: max must be positive", value.Name)
	}

	count := big.NewInt(1)
	limit := big.NewInt(int64(max))
	pointCount := len(value.SyncPoints)

	// Add one worker's ordered points at a time. For worker i, there are
	// C(i*pointCount+pointCount, pointCount) ways to insert its points among
	// the already counted steps.
	for workerIndex := 1; workerIndex < len(value.Workers); workerIndex++ {
		for pointIndex := 1; pointIndex <= pointCount; pointIndex++ {
			count.Mul(count, big.NewInt(int64(workerIndex*pointCount+pointIndex)))
			count.Quo(count, big.NewInt(int64(pointIndex)))
			if count.Cmp(limit) > 0 {
				return 0, fmt.Errorf(
					"count schedule candidates for scenario %q: more than %d",
					value.Name,
					max,
				)
			}
		}
	}

	return int(count.Int64()), nil
}

func exhaustiveSequence(value Scenario) iter.Seq[Schedule] {
	return func(yield func(Schedule) bool) {
		expectedSteps := len(value.Workers) * len(value.SyncPoints)
		nextPoint := make([]int, len(value.Workers))
		steps := make([]CoordinationStep, 0, expectedSteps)

		var walk func() bool
		walk = func() bool {
			if len(steps) == expectedSteps {
				schedule, err := NewSchedule(steps)
				if err != nil {
					panic(fmt.Sprintf("build exhaustive schedule: %v", err))
				}
				return yield(schedule)
			}

			for workerIndex, worker := range value.Workers {
				pointIndex := nextPoint[workerIndex]
				if pointIndex == len(value.SyncPoints) {
					continue
				}

				steps = append(steps, CoordinationStep{
					Worker: worker.ID,
					Point:  value.SyncPoints[pointIndex],
				})
				nextPoint[workerIndex]++
				if !walk() {
					return false
				}
				nextPoint[workerIndex]--
				steps = steps[:len(steps)-1]
			}

			return true
		}

		walk()
	}
}
