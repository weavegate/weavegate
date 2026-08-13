package syncpoint

import (
	"context"
	"errors"
	"testing"
	"time"
)

const runtimeTestTimeout = 2 * time.Second

func TestRuntimeCore(t *testing.T) {
	t.Run("wait before arrive and advance two points", func(t *testing.T) {
		runtime := newTestRuntime(t)
		ctx, cancel := context.WithTimeout(context.Background(), runtimeTestTimeout)
		defer cancel()

		if err := runtime.Register("w1"); err != nil {
			t.Fatalf("register w1: %v", err)
		}

		firstWait := waitInBackground(ctx, runtime, "w1", "after_read_request")
		waitForActiveWait(t, ctx, runtime, "w1", "after_read_request")
		firstArrival := arriveInBackground(ctx, runtime, "w1", "after_read_request")
		assertWaitStatus(t, ctx, firstWait, ArriveStatusArrived)
		assertWorkerState(t, runtime, "w1", WorkerStateArrived, "after_read_request")
		assertArrivalBlocked(t, runtime, "w1", "after_read_request", firstArrival)

		if err := runtime.Release(ctx, "w1", "after_read_request"); err != nil {
			t.Fatalf("release w1 after_read_request: %v", err)
		}
		assertArrivalResult(t, ctx, firstArrival, nil)
		assertWorkerState(t, runtime, "w1", WorkerStateReleased, "after_read_request")

		secondArrival := arriveInBackground(ctx, runtime, "w1", "before_insert_assignment")
		waitForWorkerState(
			t,
			ctx,
			runtime,
			"w1",
			WorkerStateArrived,
			"before_insert_assignment",
		)
		status, err := runtime.WaitArrive(
			ctx,
			"w1",
			"before_insert_assignment",
			runtimeTestTimeout,
		)
		if err != nil {
			t.Fatalf("wait for w1 before_insert_assignment: %v", err)
		}
		if status != ArriveStatusArrived {
			t.Fatalf("wait status = %s, want arrived", status)
		}
		assertArrivalBlocked(t, runtime, "w1", "before_insert_assignment", secondArrival)

		if err := runtime.Release(ctx, "w1", "before_insert_assignment"); err != nil {
			t.Fatalf("release w1 before_insert_assignment: %v", err)
		}
		assertArrivalResult(t, ctx, secondArrival, nil)
		if err := runtime.Finish("w1", nil); err != nil {
			t.Fatalf("finish w1: %v", err)
		}
		assertWorkerState(t, runtime, "w1", WorkerStateDone, "before_insert_assignment")
	})

	t.Run("release is targeted to one worker", func(t *testing.T) {
		runtime := newTestRuntime(t)
		ctx, cancel := context.WithTimeout(context.Background(), runtimeTestTimeout)
		defer cancel()

		for _, workerID := range []string{"w1", "w2"} {
			if err := runtime.Register(workerID); err != nil {
				t.Fatalf("register %s: %v", workerID, err)
			}
		}

		w1Arrival := arriveInBackground(ctx, runtime, "w1", "point")
		w2Arrival := arriveInBackground(ctx, runtime, "w2", "point")
		waitForWorkerState(t, ctx, runtime, "w1", WorkerStateArrived, "point")
		waitForWorkerState(t, ctx, runtime, "w2", WorkerStateArrived, "point")

		if err := runtime.Release(ctx, "w1", "point"); err != nil {
			t.Fatalf("release w1: %v", err)
		}
		assertArrivalResult(t, ctx, w1Arrival, nil)
		assertWorkerState(t, runtime, "w2", WorkerStateArrived, "point")
		assertArrivalBlocked(t, runtime, "w2", "point", w2Arrival)

		if err := runtime.Release(ctx, "w2", "point"); err != nil {
			t.Fatalf("release w2: %v", err)
		}
		assertArrivalResult(t, ctx, w2Arrival, nil)
	})

	t.Log(
		"SYNCPOINT_RUNTIME_RESULT worker=w1 points=2 targeted_release=true " +
			"terminal=done blocked_arrive_unblocked=true",
	)
}

func TestRuntimeProtocol(t *testing.T) {
	t.Run("rejects duplicate worker", func(t *testing.T) {
		runtime := newTestRuntime(t)
		if err := runtime.Register("w1"); err != nil {
			t.Fatalf("register w1: %v", err)
		}
		assertErrorIs(t, runtime.Register("w1"), ErrInvalidTransition)
	})

	t.Run("rejects unknown worker", func(t *testing.T) {
		runtime := newTestRuntime(t)
		_, err := runtime.WaitArrive(
			context.Background(),
			"missing",
			"point",
			runtimeTestTimeout,
		)
		assertErrorIs(t, err, ErrUnknownWorker)
	})

	t.Run("rejects release before arrive", func(t *testing.T) {
		runtime := newTestRuntime(t)
		if err := runtime.Register("w1"); err != nil {
			t.Fatalf("register w1: %v", err)
		}
		assertErrorIs(
			t,
			runtime.Release(context.Background(), "w1", "point"),
			ErrInvalidTransition,
		)
	})

	t.Run("rejects wrong point and duplicate release", func(t *testing.T) {
		runtime := newTestRuntime(t)
		ctx, cancel := context.WithTimeout(context.Background(), runtimeTestTimeout)
		defer cancel()

		if err := runtime.Register("w1"); err != nil {
			t.Fatalf("register w1: %v", err)
		}
		arrival := arriveInBackground(ctx, runtime, "w1", "point")
		waitForWorkerState(t, ctx, runtime, "w1", WorkerStateArrived, "point")

		assertErrorIs(
			t,
			runtime.Release(ctx, "w1", "other_point"),
			ErrPointMismatch,
		)
		if err := runtime.Release(ctx, "w1", "point"); err != nil {
			t.Fatalf("release w1: %v", err)
		}
		assertArrivalResult(t, ctx, arrival, nil)
		assertErrorIs(
			t,
			runtime.Release(ctx, "w1", "point"),
			ErrInvalidTransition,
		)
	})

	t.Run("rejects concurrent wait", func(t *testing.T) {
		runtime := newTestRuntime(t)
		ctx, cancel := context.WithTimeout(context.Background(), runtimeTestTimeout)
		defer cancel()

		if err := runtime.Register("w1"); err != nil {
			t.Fatalf("register w1: %v", err)
		}
		firstWait := waitInBackground(ctx, runtime, "w1", "point")
		waitForActiveWait(t, ctx, runtime, "w1", "point")

		_, err := runtime.WaitArrive(ctx, "w1", "point", runtimeTestTimeout)
		assertErrorIs(t, err, ErrInvalidTransition)

		arrival := arriveInBackground(ctx, runtime, "w1", "point")
		assertWaitStatus(t, ctx, firstWait, ArriveStatusArrived)
		if err := runtime.Release(ctx, "w1", "point"); err != nil {
			t.Fatalf("release w1: %v", err)
		}
		assertArrivalResult(t, ctx, arrival, nil)
	})

	t.Run("rejects stale wait and accepts next point", func(t *testing.T) {
		runtime := newTestRuntime(t)
		ctx, cancel := context.WithTimeout(context.Background(), runtimeTestTimeout)
		defer cancel()

		if err := runtime.Register("w1"); err != nil {
			t.Fatalf("register w1: %v", err)
		}
		firstArrival := arriveInBackground(ctx, runtime, "w1", "point_1")
		waitForWorkerState(t, ctx, runtime, "w1", WorkerStateArrived, "point_1")
		if err := runtime.Release(ctx, "w1", "point_1"); err != nil {
			t.Fatalf("release w1 point_1: %v", err)
		}
		assertArrivalResult(t, ctx, firstArrival, nil)

		_, err := runtime.WaitArrive(ctx, "w1", "point_1", runtimeTestTimeout)
		assertErrorIs(t, err, ErrInvalidTransition)

		nextWait := waitInBackground(ctx, runtime, "w1", "point_2")
		waitForActiveWait(t, ctx, runtime, "w1", "point_2")
		nextArrival := arriveInBackground(ctx, runtime, "w1", "point_2")
		assertWaitStatus(t, ctx, nextWait, ArriveStatusArrived)
		if err := runtime.Release(ctx, "w1", "point_2"); err != nil {
			t.Fatalf("release w1 point_2: %v", err)
		}
		assertArrivalResult(t, ctx, nextArrival, nil)
	})

	t.Log(
		"SYNCPOINT_PROTOCOL_RESULT duplicate_worker=error unknown_worker=error " +
			"release_before_arrive=error wrong_point=error duplicate_release=error " +
			"concurrent_wait=error stale_wait=error",
	)
}

type waitResult struct {
	status ArriveStatus
	err    error
}

func newTestRuntime(t *testing.T) *runtime {
	t.Helper()
	runtime := New().(*runtime)
	t.Cleanup(runtime.Close)
	return runtime
}

func arriveInBackground(
	ctx context.Context,
	runtime *runtime,
	workerID string,
	point string,
) <-chan error {
	result := make(chan error, 1)
	go func() {
		result <- runtime.Arrive(ctx, workerID, point)
	}()
	return result
}

func waitInBackground(
	ctx context.Context,
	runtime *runtime,
	workerID string,
	point string,
) <-chan waitResult {
	result := make(chan waitResult, 1)
	go func() {
		status, err := runtime.WaitArrive(ctx, workerID, point, runtimeTestTimeout)
		result <- waitResult{status: status, err: err}
	}()
	return result
}

func waitForActiveWait(
	t *testing.T,
	ctx context.Context,
	runtime *runtime,
	workerID string,
	point string,
) {
	t.Helper()

	for {
		runtime.mu.Lock()
		worker := runtime.workers[workerID]
		if worker != nil && worker.waitActive && worker.waitPoint == point {
			runtime.mu.Unlock()
			return
		}
		changed := runtime.changed
		runtime.mu.Unlock()

		select {
		case <-changed:
		case <-ctx.Done():
			t.Fatalf("wait for active wait on worker %q at %q: %v", workerID, point, ctx.Err())
		}
	}
}

func waitForWorkerState(
	t *testing.T,
	ctx context.Context,
	runtime *runtime,
	workerID string,
	wantState WorkerState,
	wantPoint string,
) {
	t.Helper()

	for {
		runtime.mu.Lock()
		worker := runtime.workers[workerID]
		if worker == nil {
			runtime.mu.Unlock()
			t.Fatalf("worker %q is not registered", workerID)
		}
		gotState := worker.state
		gotPoint := worker.point
		if gotState == wantState && gotPoint == wantPoint {
			runtime.mu.Unlock()
			return
		}
		changed := runtime.changed
		runtime.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			t.Fatalf(
				"wait for worker %q state=%s point=%q: last state=%s point=%q: %v",
				workerID,
				wantState,
				wantPoint,
				gotState,
				gotPoint,
				ctx.Err(),
			)
		}
	}
}

func assertWorkerState(
	t *testing.T,
	runtime *runtime,
	workerID string,
	wantState WorkerState,
	wantPoint string,
) {
	t.Helper()

	snapshot, err := runtime.Snapshot(workerID)
	if err != nil {
		t.Fatalf("snapshot worker %q: %v", workerID, err)
	}
	if snapshot.State != wantState || snapshot.Point != wantPoint {
		t.Fatalf(
			"worker %q state=%s point=%q, want state=%s point=%q",
			workerID,
			snapshot.State,
			snapshot.Point,
			wantState,
			wantPoint,
		)
	}
}

func assertWaitStatus(
	t *testing.T,
	ctx context.Context,
	result <-chan waitResult,
	want ArriveStatus,
) {
	t.Helper()

	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("wait arrive: %v", got.err)
		}
		if got.status != want {
			t.Fatalf("wait status = %s, want %s", got.status, want)
		}
	case <-ctx.Done():
		t.Fatalf("wait for WaitArrive result: %v", ctx.Err())
	}
}

func assertArrivalResult(
	t *testing.T,
	ctx context.Context,
	result <-chan error,
	want error,
) {
	t.Helper()

	select {
	case err := <-result:
		if !errors.Is(err, want) {
			t.Fatalf("arrival error = %v, want %v", err, want)
		}
	case <-ctx.Done():
		t.Fatalf("wait for Arrive result: %v", ctx.Err())
	}
}

func assertArrivalBlocked(
	t *testing.T,
	runtime *runtime,
	workerID string,
	point string,
	result <-chan error,
) {
	t.Helper()

	assertWorkerState(t, runtime, workerID, WorkerStateArrived, point)
	select {
	case err := <-result:
		t.Fatalf("worker %q arrival at %q returned before release: %v", workerID, point, err)
	default:
	}
}

func assertErrorIs(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("error = %v, want errors.Is(_, %v)", err, target)
	}
}
