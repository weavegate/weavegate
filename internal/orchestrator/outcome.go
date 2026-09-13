package orchestrator

import (
	"context"
	"errors"
	"fmt"

	"github.com/weavegate/weavegate/internal/sut"
	"github.com/weavegate/weavegate/internal/trace"
)

func watchSessionFaults(faults sut.SessionFaults, cancel context.CancelCauseFunc) func() {
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-faults.Done():
			cancel(faults.Err())
		case <-stop:
		}
	}()
	return func() { close(stop); <-done }
}

// receiveOutcome drains already available data even when collector shutdown is
// ready. Operation cancellation never discards a committed transaction fact.
func receiveOutcome(ctx context.Context, stream <-chan sut.InvocationOutcome) (sut.InvocationOutcome, bool, error) {
	select {
	case outcome, ok := <-stream:
		return outcome, ok, nil
	default:
	}
	select {
	case outcome, ok := <-stream:
		return outcome, ok, nil
	case <-ctx.Done():
		select {
		case outcome, ok := <-stream:
			return outcome, ok, nil
		default:
			return sut.InvocationOutcome{}, false, ctx.Err()
		}
	}
}

func (r *runCoordinator) collectInvocation(workerID string, stream <-chan sut.InvocationOutcome, collected chan<- collectedResult) {
	defer r.collectorsWait.Done()
	defer close(collected)
	var value collectedResult
	defer func() {
		collected <- value
		if value.err != nil {
			r.cancel(value.err)
		}
	}()
	outcome, ok, err := receiveOutcome(r.collectorsContext, stream)
	if err != nil {
		value.err = fmt.Errorf("worker %q result stream unfinished during cleanup", workerID)
		return
	}
	if !ok {
		value.err = fmt.Errorf("worker %q result channel closed without a result", workerID)
		return
	}
	if (outcome.Worker == nil) == (outcome.Unstarted == nil) {
		value.err = fmt.Errorf("worker %q must return exactly one outcome kind", workerID)
		return
	}
	if outcome.Worker != nil {
		if outcome.Worker.WorkerID != workerID {
			value.err = fmt.Errorf("worker %q returned result for %q", workerID, outcome.Worker.WorkerID)
			return
		}
		value.result = *outcome.Worker
		if err := r.runtime.Finish(workerID, value.result.Err); err != nil {
			value.err = fmt.Errorf("finish worker %q: %w", workerID, err)
			return
		}
	} else {
		if outcome.Unstarted.WorkerID != workerID || outcome.Unstarted.Err == nil {
			value.err = fmt.Errorf("worker %q returned invalid unstarted outcome", workerID)
			return
		}
		unstarted := *outcome.Unstarted
		value.unstarted = &unstarted
		value.err = &unstarted
		// Wake runtime waits without calling Finish for an unstarted command.
		r.cancel(value.err)
	}
	_, ok, err = receiveOutcome(r.collectorsContext, stream)
	if err != nil {
		value.err = joinRunError(value.err, fmt.Errorf("worker %q result channel did not close before cleanup", workerID))
	} else if ok {
		value.err = joinRunError(value.err, fmt.Errorf("worker %q returned more than one result", workerID))
	}
}

func (r *runCoordinator) acceptCollected(execution *workerExecution, value collectedResult, step int) error {
	execution.collected = true
	execution.result = value.result
	execution.unstarted = value.unstarted
	execution.collectionErr = value.err
	if value.result.WorkerID != "" {
		execution.terminal = true
		execution.collectionErr = joinRunError(execution.collectionErr, r.emitTerminal(execution, step))
	}
	return execution.collectionErr
}

// finalizeEvidence runs only after collectors stop. Retain known facts even on
// canceled/error paths, in scenario order rather than goroutine completion order.
func (r *runCoordinator) finalizeEvidence() error {
	var err error
	r.result.Workers = nil
	r.result.Unstarted = nil
	r.result.Terminals = nil
	for _, worker := range r.value.Workers {
		execution := r.executions[worker.ID]
		if execution == nil {
			continue
		}
		if !execution.collected && execution.collectedResult != nil {
			if value, ok := <-execution.collectedResult; ok {
				_ = r.acceptCollected(execution, value, -1)
			}
		}
		err = joinRunError(err, execution.collectionErr)
		if execution.terminal {
			r.result.Workers = append(r.result.Workers, execution.result)
			r.result.Terminals = append(r.result.Terminals, trace.WorkerTerminal{
				Worker: worker.ID, State: execution.terminalState, FailureClass: execution.failureClass,
			})
		}
		if execution.unstarted != nil {
			r.result.Unstarted = append(r.result.Unstarted, *execution.unstarted)
		}
	}
	return err
}

func joinRunError(current, next error) error {
	if next == nil || errors.Is(current, next) {
		return current
	}
	return errors.Join(current, next)
}

// Replace cancellation injected solely to wake execution with its actual cause.
// The original operation context is observed independently at finalization.
func executionError(ctx context.Context, err error) error {
	if ctx.Err() != nil && errors.Is(err, ctx.Err()) && context.Cause(ctx) != ctx.Err() {
		return &executionFailure{original: err, wakeup: ctx.Err(), cause: context.Cause(ctx)}
	}
	return err
}

// executionFailure masks only the internal wakeup sentinel. Delegating Is/As
// preserves independent joined errors and typed wrappers from the evaluator;
// replacing the entire error with context.Cause would silently discard them.
type executionFailure struct {
	original error
	wakeup   error
	cause    error
}

func (e *executionFailure) Error() string { return fmt.Sprintf("%v: %v", e.original, e.cause) }
func (e *executionFailure) Unwrap() error { return e.cause }
func (e *executionFailure) Is(target error) bool {
	return target != e.wakeup && errors.Is(e.original, target)
}
func (e *executionFailure) As(target any) bool { return errors.As(e.original, target) }
