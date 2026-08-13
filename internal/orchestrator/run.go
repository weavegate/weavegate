package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/weavegate/weavegate/internal/scenario"
	"github.com/weavegate/weavegate/internal/sut"
	"github.com/weavegate/weavegate/internal/syncpoint"
)

// RunResult contains the stable single-run outcome needed by an observer and
// the later replay layer.
type RunResult struct {
	ScheduleID      string
	Steps           int
	Workers         []sut.WorkerResult
	Timeouts        int
	PendingResolved int
	Fingerprint     string
	Elapsed         time.Duration
}

type collectedResult struct {
	result sut.WorkerResult
	err    error
}

type workerExecution struct {
	worker          scenario.Worker
	result          sut.WorkerResult
	collected       bool
	terminal        bool
	collectedResult <-chan collectedResult
}

type runCoordinator struct {
	ctx      context.Context
	runtime  syncpoint.Runtime
	handle   sut.Handle
	value    scenario.Scenario
	schedule scenario.Schedule
	result   *RunResult

	collectorsContext context.Context
	collectorsWait    *sync.WaitGroup
	blockTimeout      time.Duration
	stepTimeout       time.Duration

	executions  map[string]*workerExecution
	firstSteps  map[string]int
	preObserved map[int]bool
	pending     map[int]bool
}

// Run resets the fixture and executes one saved control schedule. Worker
// failures remain terminal run data; orchestration, protocol, observer, and
// cleanup failures are returned as errors.
func (o *Orchestrator) Run(
	ctx context.Context,
	value scenario.Scenario,
	schedule scenario.Schedule,
	observer Observer,
) (result RunResult, returnErr error) {
	startedAt := time.Now()
	result = RunResult{
		ScheduleID: schedule.ID,
		Steps:      len(schedule.Steps),
	}
	defer func() {
		result.Elapsed = time.Since(startedAt)
	}()

	if ctx == nil {
		return result, errors.New("run schedule: context is required")
	}
	if observer == nil {
		return result, errors.New("run schedule: observer is required")
	}
	if err := scenario.Validate(value, schedule); err != nil {
		return result, fmt.Errorf("run schedule %q: %w", schedule.ID, err)
	}

	runCtx, cancelRun := context.WithTimeout(ctx, o.config.RunTimeout)
	defer cancelRun()
	if err := o.config.Fixture.Reset(runCtx); err != nil {
		return result, fmt.Errorf("run schedule %q: reset fixture: %w", schedule.ID, err)
	}

	runtime := o.config.NewRuntime()
	if runtime == nil {
		return result, fmt.Errorf("run schedule %q: runtime factory returned nil", schedule.ID)
	}

	var (
		adapter        sut.Adapter
		collectorsWait sync.WaitGroup
	)
	collectorsCtx, cancelCollectors := context.WithCancel(runCtx)
	defer func() {
		cancelCollectors()
		if adapter != nil {
			stopCtx, cancelStop := context.WithTimeout(
				context.WithoutCancel(ctx),
				o.config.StopTimeout,
			)
			stopErr := adapter.Stop(stopCtx)
			cancelStop()
			if stopErr != nil {
				returnErr = errors.Join(
					returnErr,
					fmt.Errorf("run schedule %q: stop adapter: %w", schedule.ID, stopErr),
				)
			}
		}
		runtime.Close()
		collectorsWait.Wait()
	}()

	adapter = o.config.NewAdapter(runtime)
	if adapter == nil {
		return result, fmt.Errorf("run schedule %q: adapter factory returned nil", schedule.ID)
	}
	handle, err := adapter.Start(runCtx, value.Clone().SUTConfig, o.config.DB)
	if err != nil {
		return result, fmt.Errorf("run schedule %q: start adapter: %w", schedule.ID, err)
	}
	if handle == nil {
		return result, fmt.Errorf("run schedule %q: adapter start returned nil handle", schedule.ID)
	}

	coordinator := &runCoordinator{
		ctx:               runCtx,
		runtime:           runtime,
		handle:            handle,
		value:             value.Clone(),
		schedule:          schedule.Clone(),
		result:            &result,
		collectorsContext: collectorsCtx,
		collectorsWait:    &collectorsWait,
		blockTimeout:      o.config.BlockInferenceTimeout,
		stepTimeout:       o.config.StepTimeout,
		executions:        make(map[string]*workerExecution, len(value.Workers)),
		firstSteps:        make(map[string]int, len(value.Workers)),
		preObserved:       make(map[int]bool),
		pending:           make(map[int]bool),
	}
	if err := coordinator.execute(); err != nil {
		return result, fmt.Errorf("run schedule %q: %w", schedule.ID, err)
	}

	observed, err := observer(runCtx, o.config.DB, result.clone())
	if err != nil {
		return result, fmt.Errorf("run schedule %q: observe state: %w", schedule.ID, err)
	}
	result.Fingerprint = observed
	return result, nil
}

func (r *runCoordinator) execute() error {
	for index, step := range r.schedule.Steps {
		if _, exists := r.firstSteps[step.Worker]; !exists {
			r.firstSteps[step.Worker] = index
		}
	}

	for _, worker := range r.value.Workers {
		if err := r.runtime.Register(worker.ID); err != nil {
			return fmt.Errorf("register worker %q: %w", worker.ID, err)
		}
		r.executions[worker.ID] = &workerExecution{worker: worker}
	}

	for _, worker := range r.value.Workers {
		if err := r.invoke(worker); err != nil {
			return err
		}
		stepIndex := r.firstSteps[worker.ID]
		if err := r.bootstrap(stepIndex); err != nil {
			return err
		}
	}

	for index := range r.schedule.Steps {
		if err := r.traverse(index); err != nil {
			return err
		}
	}

	for len(r.pending) > 0 {
		progressed, err := r.drainPending()
		if err != nil {
			return err
		}
		if !progressed {
			return fmt.Errorf(
				"no progress while %d coordination step(s) remain pending",
				len(r.pending),
			)
		}
	}

	workers := make([]sut.WorkerResult, 0, len(r.value.Workers))
	for _, worker := range r.value.Workers {
		execution := r.executions[worker.ID]
		if !execution.collected {
			if err := r.collect(worker.ID); err != nil {
				return err
			}
		}
		workers = append(workers, execution.result)
	}
	r.result.Workers = workers
	return nil
}

func (r *runCoordinator) invoke(worker scenario.Worker) error {
	results, err := r.handle.Invoke(r.ctx, worker.ID, worker.Command)
	if err != nil {
		return fmt.Errorf("invoke worker %q command %q: %w", worker.ID, worker.Command, err)
	}
	if results == nil {
		return fmt.Errorf("invoke worker %q command %q: nil result channel", worker.ID, worker.Command)
	}

	collected := make(chan collectedResult, 1)
	r.executions[worker.ID].collectedResult = collected
	r.collectorsWait.Add(1)
	go func() {
		defer r.collectorsWait.Done()
		defer close(collected)

		select {
		case result, ok := <-results:
			if !ok {
				collected <- collectedResult{err: fmt.Errorf(
					"worker %q result channel closed without a result",
					worker.ID,
				)}
				return
			}
			if result.WorkerID != worker.ID {
				collected <- collectedResult{err: fmt.Errorf(
					"worker %q returned result for %q",
					worker.ID,
					result.WorkerID,
				)}
				return
			}
			if err := r.runtime.Finish(result.WorkerID, result.Err); err != nil {
				collected <- collectedResult{err: fmt.Errorf(
					"finish worker %q: %w",
					worker.ID,
					err,
				)}
				return
			}
			collected <- collectedResult{result: result}
		case <-r.collectorsContext.Done():
			return
		}
	}()
	return nil
}

func (r *runCoordinator) bootstrap(index int) error {
	step := r.schedule.Steps[index]
	status, err := r.runtime.WaitArrive(
		r.ctx,
		step.Worker,
		step.Point,
		r.configuredBlockTimeout(),
	)
	if err != nil {
		return fmt.Errorf("bootstrap worker %q at %q: %w", step.Worker, step.Point, err)
	}
	return r.recordObservation(index, status, true)
}

func (r *runCoordinator) traverse(index int) error {
	step := r.schedule.Steps[index]
	execution := r.executions[step.Worker]
	if execution.terminal {
		return nil
	}
	if r.pending[index] {
		return nil
	}
	if r.preObserved[index] {
		delete(r.preObserved, index)
		return r.release(index)
	}

	status, err := r.runtime.WaitArrive(r.ctx, step.Worker, step.Point, r.configuredStepTimeout())
	if err != nil {
		return fmt.Errorf("wait for step[%d] worker %q at %q: %w", index, step.Worker, step.Point, err)
	}
	return r.recordObservation(index, status, false)
}

func (r *runCoordinator) recordObservation(index int, status syncpoint.ArriveStatus, bootstrap bool) error {
	step := r.schedule.Steps[index]
	switch status {
	case syncpoint.ArriveStatusArrived:
		if bootstrap {
			r.preObserved[index] = true
			return nil
		}
		return r.release(index)
	case syncpoint.ArriveStatusTimeout:
		if err := r.pollCollector(step.Worker); err != nil {
			return err
		}
		r.pending[index] = true
		r.result.Timeouts++
		return nil
	case syncpoint.ArriveStatusDone, syncpoint.ArriveStatusFailed:
		if err := r.collect(step.Worker); err != nil {
			return err
		}
		_, err := r.drainPending()
		return err
	case syncpoint.ArriveStatusUnknown:
		return fmt.Errorf(
			"step[%d] worker %q at %q returned unknown arrival status",
			index,
			step.Worker,
			step.Point,
		)
	default:
		return fmt.Errorf(
			"step[%d] worker %q at %q returned unsupported arrival status %d",
			index,
			step.Worker,
			step.Point,
			status,
		)
	}
}

func (r *runCoordinator) release(index int) error {
	step := r.schedule.Steps[index]
	if err := r.runtime.Release(r.ctx, step.Worker, step.Point); err != nil {
		return fmt.Errorf("release step[%d] worker %q at %q: %w", index, step.Worker, step.Point, err)
	}

	if step.Point != r.value.SyncPoints[len(r.value.SyncPoints)-1] {
		return nil
	}
	if err := r.collect(step.Worker); err != nil {
		return err
	}
	_, err := r.drainPending()
	return err
}

func (r *runCoordinator) drainPending() (bool, error) {
	progressed := false
	for index := range r.schedule.Steps {
		if !r.pending[index] {
			continue
		}

		step := r.schedule.Steps[index]
		execution := r.executions[step.Worker]
		if execution.terminal {
			delete(r.pending, index)
			progressed = true
			continue
		}

		status, err := r.runtime.WaitArrive(r.ctx, step.Worker, step.Point, r.configuredStepTimeout())
		if err != nil {
			return progressed, fmt.Errorf(
				"recheck pending step[%d] worker %q at %q: %w",
				index,
				step.Worker,
				step.Point,
				err,
			)
		}
		switch status {
		case syncpoint.ArriveStatusArrived:
			delete(r.pending, index)
			if err := r.releasePending(index); err != nil {
				return progressed, err
			}
			r.result.PendingResolved++
			progressed = true
		case syncpoint.ArriveStatusDone, syncpoint.ArriveStatusFailed:
			if err := r.collect(step.Worker); err != nil {
				return progressed, err
			}
			delete(r.pending, index)
			progressed = true
		case syncpoint.ArriveStatusTimeout:
			if err := r.pollCollector(step.Worker); err != nil {
				return progressed, err
			}
		case syncpoint.ArriveStatusUnknown:
			return progressed, fmt.Errorf(
				"pending step[%d] worker %q at %q returned unknown arrival status",
				index,
				step.Worker,
				step.Point,
			)
		default:
			return progressed, fmt.Errorf(
				"pending step[%d] worker %q at %q returned unsupported arrival status %d",
				index,
				step.Worker,
				step.Point,
				status,
			)
		}
	}
	return progressed, nil
}

func (r *runCoordinator) releasePending(index int) error {
	step := r.schedule.Steps[index]
	if err := r.runtime.Release(r.ctx, step.Worker, step.Point); err != nil {
		return fmt.Errorf(
			"release pending step[%d] worker %q at %q: %w",
			index,
			step.Worker,
			step.Point,
			err,
		)
	}
	if step.Point == r.value.SyncPoints[len(r.value.SyncPoints)-1] {
		return r.collect(step.Worker)
	}
	return nil
}

func (r *runCoordinator) collect(workerID string) error {
	execution := r.executions[workerID]
	if execution.collected {
		return nil
	}
	if execution.collectedResult == nil {
		return fmt.Errorf("collect worker %q before invocation", workerID)
	}

	select {
	case collected, ok := <-execution.collectedResult:
		if !ok {
			return fmt.Errorf("collect worker %q: collector closed without a result", workerID)
		}
		if collected.err != nil {
			return fmt.Errorf("collect worker %q: %w", workerID, collected.err)
		}
		execution.result = collected.result
		execution.collected = true
		execution.terminal = true
		return nil
	case <-r.ctx.Done():
		return fmt.Errorf("collect worker %q: %w", workerID, r.ctx.Err())
	}
}

func (r *runCoordinator) pollCollector(workerID string) error {
	execution := r.executions[workerID]
	if execution.collected || execution.collectedResult == nil {
		return nil
	}

	select {
	case collected, ok := <-execution.collectedResult:
		if !ok {
			return fmt.Errorf("collect worker %q: collector closed without a result", workerID)
		}
		if collected.err != nil {
			return fmt.Errorf("collect worker %q: %w", workerID, collected.err)
		}
		execution.result = collected.result
		execution.collected = true
		execution.terminal = true
		return nil
	default:
		return nil
	}
}

func (r *runCoordinator) configuredBlockTimeout() time.Duration {
	return r.blockTimeout
}

func (r *runCoordinator) configuredStepTimeout() time.Duration {
	return r.stepTimeout
}

func (r RunResult) clone() RunResult {
	clone := r
	clone.Workers = append([]sut.WorkerResult(nil), r.Workers...)
	return clone
}
