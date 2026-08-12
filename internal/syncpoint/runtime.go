// Package syncpoint coordinates named execution points between workers and an
// orchestrator.
package syncpoint

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var (
	// ErrClosed reports an operation attempted after the runtime was closed.
	ErrClosed = errors.New("sync-point runtime is closed")
	// ErrUnknownWorker reports an operation for a worker that was not registered.
	ErrUnknownWorker = errors.New("unknown sync-point worker")
	// ErrInvalidTransition reports an operation that is invalid in the current state.
	ErrInvalidTransition = errors.New("invalid sync-point transition")
	// ErrPointMismatch reports an operation for a point other than the expected point.
	ErrPointMismatch = errors.New("sync-point mismatch")
)

// WorkerState describes the latest lifecycle state observed for one worker.
type WorkerState uint8

const (
	WorkerStateUnknown WorkerState = iota
	WorkerStateRunning
	WorkerStateArrived
	WorkerStateReleased
	WorkerStateDBBlocked
	WorkerStateDone
	WorkerStateFailed
)

// ArriveStatus describes the result of waiting for a worker to arrive.
type ArriveStatus uint8

const (
	ArriveStatusUnknown ArriveStatus = iota
	ArriveStatusArrived
	ArriveStatusDone
	ArriveStatusFailed
	ArriveStatusTimeout
)

// WorkerSnapshot is an immutable view of one worker's current runtime state.
type WorkerSnapshot struct {
	WorkerID string
	State    WorkerState
	Point    string
	Err      error
}

// Client is the worker-facing sync-point surface.
type Client interface {
	Arrive(ctx context.Context, workerID, point string) error
}

// Runtime coordinates workers from registration through terminal state.
type Runtime interface {
	Client
	Register(workerID string) error
	WaitArrive(
		ctx context.Context,
		workerID string,
		point string,
		timeout time.Duration,
	) (ArriveStatus, error)
	Release(ctx context.Context, workerID, point string) error
	Finish(workerID string, workerErr error) error
	Snapshot(workerID string) (WorkerSnapshot, error)
	Close()
}

type arrival struct {
	released bool
	closed   bool
	ready    chan struct{}
}

type workerState struct {
	state WorkerState
	point string
	err   error

	arrival    *arrival
	waitActive bool
	waitPoint  string
}

type runtime struct {
	mu sync.Mutex

	closed  bool
	workers map[string]*workerState
	changed chan struct{}
}

var (
	_ Client  = (*runtime)(nil)
	_ Runtime = (*runtime)(nil)
)

// New creates one runtime for one schedule execution.
func New() Runtime {
	return &runtime{
		workers: make(map[string]*workerState),
		changed: make(chan struct{}),
	}
}

// Register adds one worker in the running state.
func (r *runtime) Register(workerID string) error {
	if strings.TrimSpace(workerID) == "" {
		return fmt.Errorf("register sync-point worker: worker ID is required: %w", ErrInvalidTransition)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return fmt.Errorf("register sync-point worker %q: %w", workerID, ErrClosed)
	}
	if _, exists := r.workers[workerID]; exists {
		return fmt.Errorf("register sync-point worker %q twice: %w", workerID, ErrInvalidTransition)
	}

	r.workers[workerID] = &workerState{state: WorkerStateRunning}
	r.signalChangedLocked()
	return nil
}

// Arrive records a worker at a named point and blocks until that exact arrival
// is released.
func (r *runtime) Arrive(ctx context.Context, workerID, point string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("worker %q arrive at %q: %w", workerID, point, err)
	}
	if err := validateWorkerAndPoint(workerID, point); err != nil {
		return fmt.Errorf("arrive: %w", err)
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return fmt.Errorf("worker %q arrive at %q: %w", workerID, point, ErrClosed)
	}
	worker, ok := r.workers[workerID]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("worker %q arrive at %q: %w", workerID, point, ErrUnknownWorker)
	}
	if err := validateArrivalLocked(worker, workerID, point); err != nil {
		r.mu.Unlock()
		return err
	}

	currentArrival := &arrival{ready: make(chan struct{})}
	worker.state = WorkerStateArrived
	worker.point = point
	worker.arrival = currentArrival
	r.signalChangedLocked()
	r.mu.Unlock()

	select {
	case <-currentArrival.ready:
		return r.resolveArrivalWake(ctx, workerID, point, worker, currentArrival)
	case <-ctx.Done():
		return r.resolveArrivalWake(ctx, workerID, point, worker, currentArrival)
	}
}

// WaitArrive waits until one registered worker reaches the requested point or
// reaches a terminal state.
func (r *runtime) WaitArrive(
	ctx context.Context,
	workerID string,
	point string,
	timeout time.Duration,
) (ArriveStatus, error) {
	if err := ctx.Err(); err != nil {
		return ArriveStatusUnknown, fmt.Errorf("wait for worker %q at %q: %w", workerID, point, err)
	}
	if err := validateWorkerAndPoint(workerID, point); err != nil {
		return ArriveStatusUnknown, fmt.Errorf("wait arrive: %w", err)
	}
	if timeout <= 0 {
		return ArriveStatusUnknown, fmt.Errorf(
			"wait for worker %q at %q with timeout %s: %w",
			workerID,
			point,
			timeout,
			ErrInvalidTransition,
		)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return ArriveStatusUnknown, fmt.Errorf("wait for worker %q at %q: %w", workerID, point, ErrClosed)
	}
	worker, ok := r.workers[workerID]
	if !ok {
		r.mu.Unlock()
		return ArriveStatusUnknown, fmt.Errorf("wait for worker %q at %q: %w", workerID, point, ErrUnknownWorker)
	}
	if worker.waitActive {
		r.mu.Unlock()
		return ArriveStatusUnknown, fmt.Errorf(
			"wait for worker %q at %q while already waiting at %q: %w",
			workerID,
			point,
			worker.waitPoint,
			ErrInvalidTransition,
		)
	}
	if status, complete, err := r.waitStatusLocked(worker, workerID, point); complete {
		r.mu.Unlock()
		return status, err
	}

	worker.waitActive = true
	worker.waitPoint = point
	r.signalChangedLocked()

	for {
		changed := r.changed
		r.mu.Unlock()

		wake := waitWakeChanged
		select {
		case <-changed:
		case <-timer.C:
			wake = waitWakeTimeout
		case <-ctx.Done():
			wake = waitWakeContext
		}

		r.mu.Lock()
		status, complete, err := r.resolveWaitWakeLocked(
			ctx,
			worker,
			workerID,
			point,
			wake,
		)
		if complete {
			r.clearWaitLocked(worker)
			r.mu.Unlock()
			return status, err
		}
	}
}

// Release advances one worker from the exact point where it is currently
// blocked.
func (r *runtime) Release(ctx context.Context, workerID, point string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("release worker %q at %q: %w", workerID, point, err)
	}
	if err := validateWorkerAndPoint(workerID, point); err != nil {
		return fmt.Errorf("release: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return fmt.Errorf("release worker %q at %q: %w", workerID, point, ErrClosed)
	}
	worker, ok := r.workers[workerID]
	if !ok {
		return fmt.Errorf("release worker %q at %q: %w", workerID, point, ErrUnknownWorker)
	}
	if worker.state != WorkerStateArrived || worker.arrival == nil {
		return fmt.Errorf(
			"release worker %q at %q from state %s: %w",
			workerID,
			point,
			worker.state,
			ErrInvalidTransition,
		)
	}
	if worker.point != point {
		return fmt.Errorf(
			"release worker %q at %q while arrived at %q: %w",
			workerID,
			point,
			worker.point,
			ErrPointMismatch,
		)
	}

	worker.state = WorkerStateReleased
	worker.arrival.released = true
	close(worker.arrival.ready)
	r.signalChangedLocked()
	return nil
}

// Finish records a terminal worker result. A worker blocked in Arrive must be
// released or canceled before it can finish.
func (r *runtime) Finish(workerID string, workerErr error) error {
	if strings.TrimSpace(workerID) == "" {
		return fmt.Errorf("finish sync-point worker: worker ID is required: %w", ErrInvalidTransition)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	worker, ok := r.workers[workerID]
	if !ok {
		return fmt.Errorf("finish worker %q: %w", workerID, ErrUnknownWorker)
	}
	switch worker.state {
	case WorkerStateRunning, WorkerStateReleased, WorkerStateDBBlocked:
	case WorkerStateArrived, WorkerStateDone, WorkerStateFailed, WorkerStateUnknown:
		return fmt.Errorf(
			"finish worker %q from state %s: %w",
			workerID,
			worker.state,
			ErrInvalidTransition,
		)
	default:
		return fmt.Errorf("finish worker %q from unknown state: %w", workerID, ErrInvalidTransition)
	}

	if workerErr == nil {
		worker.state = WorkerStateDone
	} else {
		worker.state = WorkerStateFailed
	}
	worker.err = workerErr
	worker.arrival = nil
	r.signalChangedLocked()
	return nil
}

// Snapshot returns a value copy of one worker's current state.
func (r *runtime) Snapshot(workerID string) (WorkerSnapshot, error) {
	if strings.TrimSpace(workerID) == "" {
		return WorkerSnapshot{}, fmt.Errorf(
			"snapshot sync-point worker: worker ID is required: %w",
			ErrInvalidTransition,
		)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	worker, ok := r.workers[workerID]
	if !ok {
		return WorkerSnapshot{}, fmt.Errorf("snapshot worker %q: %w", workerID, ErrUnknownWorker)
	}
	return WorkerSnapshot{
		WorkerID: workerID,
		State:    worker.state,
		Point:    worker.point,
		Err:      worker.err,
	}, nil
}

// Close unblocks runtime waiters. It is safe to call more than once.
func (r *runtime) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return
	}
	r.closed = true
	for _, worker := range r.workers {
		if worker.state != WorkerStateArrived || worker.arrival == nil {
			continue
		}
		worker.arrival.closed = true
		close(worker.arrival.ready)
		worker.arrival = nil
		worker.state = WorkerStateRunning
		worker.point = ""
	}
	r.signalChangedLocked()
}

type waitWake uint8

const (
	waitWakeChanged waitWake = iota
	waitWakeTimeout
	waitWakeContext
)

func (r *runtime) resolveWaitWakeLocked(
	ctx context.Context,
	worker *workerState,
	workerID string,
	point string,
	wake waitWake,
) (ArriveStatus, bool, error) {
	if status, complete, err := r.waitStatusLocked(worker, workerID, point); complete {
		return status, true, err
	}
	if err := ctx.Err(); err != nil {
		return ArriveStatusUnknown, true, fmt.Errorf(
			"wait for worker %q at %q: %w",
			workerID,
			point,
			err,
		)
	}
	if wake != waitWakeTimeout {
		return ArriveStatusUnknown, false, nil
	}

	worker.state = WorkerStateDBBlocked
	worker.point = point
	r.signalChangedLocked()
	return ArriveStatusTimeout, true, nil
}

func (r *runtime) waitStatusLocked(
	worker *workerState,
	workerID string,
	point string,
) (ArriveStatus, bool, error) {
	if r.closed {
		return ArriveStatusUnknown, true, fmt.Errorf(
			"wait for worker %q at %q: %w",
			workerID,
			point,
			ErrClosed,
		)
	}

	switch worker.state {
	case WorkerStateRunning:
		return ArriveStatusUnknown, false, nil
	case WorkerStateArrived:
		if worker.point != point {
			return ArriveStatusUnknown, true, fmt.Errorf(
				"wait for worker %q at %q while arrived at %q: %w",
				workerID,
				point,
				worker.point,
				ErrPointMismatch,
			)
		}
		return ArriveStatusArrived, true, nil
	case WorkerStateReleased:
		if worker.point == point {
			return ArriveStatusUnknown, true, fmt.Errorf(
				"wait for worker %q at already released point %q: %w",
				workerID,
				point,
				ErrInvalidTransition,
			)
		}
		return ArriveStatusUnknown, false, nil
	case WorkerStateDBBlocked:
		if worker.point != point {
			return ArriveStatusUnknown, true, fmt.Errorf(
				"wait for worker %q at %q while blocked before %q: %w",
				workerID,
				point,
				worker.point,
				ErrPointMismatch,
			)
		}
		return ArriveStatusUnknown, false, nil
	case WorkerStateDone:
		return ArriveStatusDone, true, nil
	case WorkerStateFailed:
		return ArriveStatusFailed, true, nil
	case WorkerStateUnknown:
		return ArriveStatusUnknown, true, fmt.Errorf(
			"wait for worker %q at %q from unknown state: %w",
			workerID,
			point,
			ErrInvalidTransition,
		)
	default:
		return ArriveStatusUnknown, true, fmt.Errorf(
			"wait for worker %q at %q from unsupported state: %w",
			workerID,
			point,
			ErrInvalidTransition,
		)
	}
}

func (r *runtime) clearWaitLocked(worker *workerState) {
	worker.waitActive = false
	worker.waitPoint = ""
	r.signalChangedLocked()
}

func (r *runtime) resolveArrivalWake(
	ctx context.Context,
	workerID string,
	point string,
	worker *workerState,
	currentArrival *arrival,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if currentArrival.released {
		if worker.arrival == currentArrival {
			worker.arrival = nil
		}
		return nil
	}
	if currentArrival.closed || r.closed {
		return fmt.Errorf("worker %q arrive at %q: %w", workerID, point, ErrClosed)
	}
	if err := ctx.Err(); err != nil {
		if worker.arrival == currentArrival && worker.state == WorkerStateArrived {
			worker.arrival = nil
			worker.state = WorkerStateRunning
			worker.point = ""
			r.signalChangedLocked()
		}
		return fmt.Errorf("worker %q arrive at %q: %w", workerID, point, err)
	}

	return fmt.Errorf(
		"worker %q arrive at %q woke without release: %w",
		workerID,
		point,
		ErrInvalidTransition,
	)
}

func validateArrivalLocked(worker *workerState, workerID, point string) error {
	if worker.waitActive && worker.waitPoint != point {
		return fmt.Errorf(
			"worker %q arrive at %q while awaited at %q: %w",
			workerID,
			point,
			worker.waitPoint,
			ErrPointMismatch,
		)
	}

	switch worker.state {
	case WorkerStateRunning:
		return nil
	case WorkerStateDBBlocked:
		if worker.point != point {
			return fmt.Errorf(
				"worker %q arrive at %q while blocked before %q: %w",
				workerID,
				point,
				worker.point,
				ErrPointMismatch,
			)
		}
		return nil
	case WorkerStateReleased:
		if worker.point == point {
			return fmt.Errorf(
				"worker %q arrive twice at released point %q: %w",
				workerID,
				point,
				ErrInvalidTransition,
			)
		}
		return nil
	case WorkerStateArrived:
		if worker.point != point {
			return fmt.Errorf(
				"worker %q arrive at %q while already arrived at %q: %w",
				workerID,
				point,
				worker.point,
				ErrPointMismatch,
			)
		}
		return fmt.Errorf(
			"worker %q arrive twice at point %q: %w",
			workerID,
			point,
			ErrInvalidTransition,
		)
	case WorkerStateDone, WorkerStateFailed, WorkerStateUnknown:
		return fmt.Errorf(
			"worker %q arrive at %q from state %s: %w",
			workerID,
			point,
			worker.state,
			ErrInvalidTransition,
		)
	default:
		return fmt.Errorf(
			"worker %q arrive at %q from unsupported state: %w",
			workerID,
			point,
			ErrInvalidTransition,
		)
	}
}

func validateWorkerAndPoint(workerID, point string) error {
	if strings.TrimSpace(workerID) == "" {
		return fmt.Errorf("worker ID is required: %w", ErrInvalidTransition)
	}
	if strings.TrimSpace(point) == "" {
		return fmt.Errorf("point is required for worker %q: %w", workerID, ErrInvalidTransition)
	}
	return nil
}

func (r *runtime) signalChangedLocked() {
	close(r.changed)
	r.changed = make(chan struct{})
}

func (s WorkerState) String() string {
	switch s {
	case WorkerStateRunning:
		return "running"
	case WorkerStateArrived:
		return "arrived"
	case WorkerStateReleased:
		return "released"
	case WorkerStateDBBlocked:
		return "db_blocked"
	case WorkerStateDone:
		return "done"
	case WorkerStateFailed:
		return "failed"
	case WorkerStateUnknown:
		return "unknown"
	default:
		return "unsupported"
	}
}

func (s ArriveStatus) String() string {
	switch s {
	case ArriveStatusArrived:
		return "arrived"
	case ArriveStatusDone:
		return "done"
	case ArriveStatusFailed:
		return "failed"
	case ArriveStatusTimeout:
		return "timeout"
	case ArriveStatusUnknown:
		return "unknown"
	default:
		return "unsupported"
	}
}
