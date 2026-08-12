package syncpoint

import (
	"context"
	"errors"
	"testing"
	"time"
)

const runtimeInferenceTimeout = 10 * time.Millisecond

func TestRuntimeTimeout(t *testing.T) {
	t.Run("resumes a timeout-inferred worker", func(t *testing.T) {
		runtime := newTestRuntime(t)
		ctx, cancel := context.WithTimeout(context.Background(), runtimeTestTimeout)
		defer cancel()

		if err := runtime.Register("w2"); err != nil {
			t.Fatalf("register w2: %v", err)
		}

		status, err := runtime.WaitArrive(
			ctx,
			"w2",
			"after_read_request",
			runtimeInferenceTimeout,
		)
		if err != nil {
			t.Fatalf("wait for timeout-inferred w2: %v", err)
		}
		if status != ArriveStatusTimeout {
			t.Fatalf("wait status = %s, want timeout", status)
		}
		assertWorkerState(t, runtime, "w2", WorkerStateDBBlocked, "after_read_request")

		assertErrorIs(
			t,
			runtime.Arrive(ctx, "w2", "before_insert_assignment"),
			ErrPointMismatch,
		)
		_, err = runtime.WaitArrive(
			ctx,
			"w2",
			"before_insert_assignment",
			runtimeTestTimeout,
		)
		assertErrorIs(t, err, ErrPointMismatch)
		assertWorkerState(t, runtime, "w2", WorkerStateDBBlocked, "after_read_request")

		arrival := arriveInBackground(ctx, runtime, "w2", "after_read_request")
		waitForWorkerState(t, ctx, runtime, "w2", WorkerStateArrived, "after_read_request")
		status, err = runtime.WaitArrive(
			ctx,
			"w2",
			"after_read_request",
			runtimeTestTimeout,
		)
		if err != nil {
			t.Fatalf("wait for resumed w2: %v", err)
		}
		if status != ArriveStatusArrived {
			t.Fatalf("resumed wait status = %s, want arrived", status)
		}

		if err := runtime.Release(ctx, "w2", "after_read_request"); err != nil {
			t.Fatalf("release resumed w2: %v", err)
		}
		assertArrivalResult(t, ctx, arrival, nil)
		if err := runtime.Finish("w2", nil); err != nil {
			t.Fatalf("finish resumed w2: %v", err)
		}
		assertWorkerState(t, runtime, "w2", WorkerStateDone, "after_read_request")
	})

	t.Run("timeout wake rechecks an arrived state", func(t *testing.T) {
		runtime := newTestRuntime(t)
		ctx, cancel := context.WithTimeout(context.Background(), runtimeTestTimeout)
		defer cancel()

		if err := runtime.Register("w1"); err != nil {
			t.Fatalf("register w1: %v", err)
		}
		arrival := arriveInBackground(ctx, runtime, "w1", "point")
		waitForWorkerState(t, ctx, runtime, "w1", WorkerStateArrived, "point")

		runtime.mu.Lock()
		worker := runtime.workers["w1"]
		status, complete, err := runtime.resolveWaitWakeLocked(
			ctx,
			worker,
			"w1",
			"point",
			waitWakeTimeout,
		)
		runtime.mu.Unlock()
		if err != nil {
			t.Fatalf("resolve timeout wake after arrival: %v", err)
		}
		if !complete {
			t.Fatal("resolve timeout wake complete = false, want true")
		}
		if status != ArriveStatusArrived {
			t.Fatalf("resolved status = %s, want arrived", status)
		}
		assertWorkerState(t, runtime, "w1", WorkerStateArrived, "point")

		if err := runtime.Release(ctx, "w1", "point"); err != nil {
			t.Fatalf("release w1: %v", err)
		}
		assertArrivalResult(t, ctx, arrival, nil)
	})

	t.Run("caller cancellation is not a runtime timeout", func(t *testing.T) {
		runtime := newTestRuntime(t)
		testCtx, stop := context.WithTimeout(context.Background(), runtimeTestTimeout)
		defer stop()
		if err := runtime.Register("w1"); err != nil {
			t.Fatalf("register w1: %v", err)
		}

		waitCtx, cancelWait := context.WithCancel(testCtx)
		result := waitInBackground(waitCtx, runtime, "w1", "point")
		waitForActiveWait(t, testCtx, runtime, "w1", "point")
		cancelWait()

		got := awaitWaitResult(t, testCtx, result)
		assertErrorIs(t, got.err, context.Canceled)
		if got.status != ArriveStatusUnknown {
			t.Fatalf("canceled wait status = %s, want unknown", got.status)
		}
		assertWorkerState(t, runtime, "w1", WorkerStateRunning, "")
	})

	t.Log(
		"SYNCPOINT_TIMEOUT_RESULT worker=w2 wait_status=timeout state=db_blocked " +
			"inference=timeout resumed=arrived terminal=done",
	)
}

func TestRuntimeCancellation(t *testing.T) {
	t.Run("cancellation wins before release", func(t *testing.T) {
		runtime := newTestRuntime(t)
		testCtx, stop := context.WithTimeout(context.Background(), runtimeTestTimeout)
		defer stop()
		ctx, cancel := context.WithCancel(testCtx)

		if err := runtime.Register("w1"); err != nil {
			t.Fatalf("register w1: %v", err)
		}
		arrival := arriveInBackground(ctx, runtime, "w1", "point")
		waitForWorkerState(t, testCtx, runtime, "w1", WorkerStateArrived, "point")

		cancel()
		assertArrivalResult(t, testCtx, arrival, context.Canceled)
		assertWorkerState(t, runtime, "w1", WorkerStateRunning, "")
		assertErrorIs(
			t,
			runtime.Release(context.Background(), "w1", "point"),
			ErrInvalidTransition,
		)
		if err := runtime.Finish("w1", context.Canceled); err != nil {
			t.Fatalf("finish canceled w1: %v", err)
		}
		assertWorkerError(t, runtime, "w1", WorkerStateFailed, context.Canceled)
	})

	t.Run("release wins before cancellation", func(t *testing.T) {
		runtime := newTestRuntime(t)
		testCtx, stop := context.WithTimeout(context.Background(), runtimeTestTimeout)
		defer stop()
		ctx, cancel := context.WithCancel(testCtx)
		defer cancel()

		if err := runtime.Register("w1"); err != nil {
			t.Fatalf("register w1: %v", err)
		}
		arrival := arriveInBackground(ctx, runtime, "w1", "point")
		waitForWorkerState(t, testCtx, runtime, "w1", WorkerStateArrived, "point")

		if err := runtime.Release(testCtx, "w1", "point"); err != nil {
			t.Fatalf("release w1: %v", err)
		}
		cancel()
		assertArrivalResult(t, testCtx, arrival, nil)
		assertWorkerState(t, runtime, "w1", WorkerStateReleased, "point")
	})

	t.Log(
		"SYNCPOINT_CANCEL_RESULT worker=w1 canceled_arrival=cleared " +
			"release_after_cancel=error release_wins=nil ghost_arrival=false",
	)
}

func TestRuntimeTerminal(t *testing.T) {
	t.Run("records done", func(t *testing.T) {
		runtime := newTestRuntime(t)
		if err := runtime.Register("w1"); err != nil {
			t.Fatalf("register w1: %v", err)
		}
		if err := runtime.Finish("w1", nil); err != nil {
			t.Fatalf("finish w1: %v", err)
		}

		status, err := runtime.WaitArrive(
			context.Background(),
			"w1",
			"unused_point",
			runtimeTestTimeout,
		)
		if err != nil {
			t.Fatalf("wait for done w1: %v", err)
		}
		if status != ArriveStatusDone {
			t.Fatalf("done wait status = %s, want done", status)
		}
		assertWorkerState(t, runtime, "w1", WorkerStateDone, "")
		assertErrorIs(t, runtime.Finish("w1", nil), ErrInvalidTransition)
		assertErrorIs(
			t,
			runtime.Arrive(context.Background(), "w1", "point"),
			ErrInvalidTransition,
		)
		assertErrorIs(
			t,
			runtime.Release(context.Background(), "w1", "point"),
			ErrInvalidTransition,
		)
	})

	t.Run("preserves a failure from db-blocked", func(t *testing.T) {
		runtime := newTestRuntime(t)
		if err := runtime.Register("w2"); err != nil {
			t.Fatalf("register w2: %v", err)
		}

		status, err := runtime.WaitArrive(
			context.Background(),
			"w2",
			"point",
			runtimeInferenceTimeout,
		)
		if err != nil {
			t.Fatalf("wait for timeout-inferred w2: %v", err)
		}
		if status != ArriveStatusTimeout {
			t.Fatalf("wait status = %s, want timeout", status)
		}

		workerFailure := errors.New("worker command failed")
		if err := runtime.Finish("w2", workerFailure); err != nil {
			t.Fatalf("finish failed w2: %v", err)
		}
		assertWorkerError(t, runtime, "w2", WorkerStateFailed, workerFailure)

		status, err = runtime.WaitArrive(
			context.Background(),
			"w2",
			"later_point",
			runtimeTestTimeout,
		)
		if err != nil {
			t.Fatalf("wait for failed w2: %v", err)
		}
		if status != ArriveStatusFailed {
			t.Fatalf("failed wait status = %s, want failed", status)
		}
	})

	t.Run("rejects finish while arrived", func(t *testing.T) {
		runtime := newTestRuntime(t)
		testCtx, stop := context.WithTimeout(context.Background(), runtimeTestTimeout)
		defer stop()
		ctx, cancel := context.WithCancel(testCtx)

		if err := runtime.Register("w1"); err != nil {
			t.Fatalf("register w1: %v", err)
		}
		arrival := arriveInBackground(ctx, runtime, "w1", "point")
		waitForWorkerState(t, testCtx, runtime, "w1", WorkerStateArrived, "point")
		assertErrorIs(t, runtime.Finish("w1", nil), ErrInvalidTransition)

		cancel()
		assertArrivalResult(t, testCtx, arrival, context.Canceled)
	})
}

func TestRuntimeClose(t *testing.T) {
	runtime := newTestRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), runtimeTestTimeout)
	defer cancel()

	for _, workerID := range []string{"w1", "w2", "w3"} {
		if err := runtime.Register(workerID); err != nil {
			t.Fatalf("register %s: %v", workerID, err)
		}
	}

	w1Arrival := arriveInBackground(ctx, runtime, "w1", "point_1")
	w2Arrival := arriveInBackground(ctx, runtime, "w2", "point_2")
	w3Wait := waitInBackground(ctx, runtime, "w3", "point_3")
	waitForWorkerState(t, ctx, runtime, "w1", WorkerStateArrived, "point_1")
	waitForWorkerState(t, ctx, runtime, "w2", WorkerStateArrived, "point_2")
	waitForActiveWait(t, ctx, runtime, "w3", "point_3")

	runtime.Close()
	assertArrivalResult(t, ctx, w1Arrival, ErrClosed)
	assertArrivalResult(t, ctx, w2Arrival, ErrClosed)
	waitResult := awaitWaitResult(t, ctx, w3Wait)
	assertErrorIs(t, waitResult.err, ErrClosed)
	if waitResult.status != ArriveStatusUnknown {
		t.Fatalf("closed wait status = %s, want unknown", waitResult.status)
	}
	assertWorkerState(t, runtime, "w1", WorkerStateRunning, "")
	assertWorkerState(t, runtime, "w2", WorkerStateRunning, "")
	assertWorkerState(t, runtime, "w3", WorkerStateRunning, "")

	closeFailure := errors.New("runtime closed during worker execution")
	if err := runtime.Finish("w1", closeFailure); err != nil {
		t.Fatalf("finish closed w1: %v", err)
	}
	if err := runtime.Finish("w2", nil); err != nil {
		t.Fatalf("finish closed w2: %v", err)
	}
	if err := runtime.Finish("w3", nil); err != nil {
		t.Fatalf("finish closed w3: %v", err)
	}
	assertWorkerError(t, runtime, "w1", WorkerStateFailed, closeFailure)
	assertWorkerState(t, runtime, "w2", WorkerStateDone, "")
	assertWorkerState(t, runtime, "w3", WorkerStateDone, "")

	runtime.Close()
	assertErrorIs(t, runtime.Register("w4"), ErrClosed)
	assertErrorIs(
		t,
		runtime.Arrive(context.Background(), "w2", "late_point"),
		ErrClosed,
	)
	_, err := runtime.WaitArrive(
		context.Background(),
		"w2",
		"late_point",
		runtimeTestTimeout,
	)
	assertErrorIs(t, err, ErrClosed)
	assertErrorIs(
		t,
		runtime.Release(context.Background(), "w2", "late_point"),
		ErrClosed,
	)

	t.Log(
		"SYNCPOINT_CLOSE_RESULT blocked_arrivers=2 blocked_waiters=1 " +
			"unblocked=3 close_idempotent=true",
	)
}

func awaitWaitResult(
	t *testing.T,
	ctx context.Context,
	result <-chan waitResult,
) waitResult {
	t.Helper()

	select {
	case got := <-result:
		return got
	case <-ctx.Done():
		t.Fatalf("wait for WaitArrive result: %v", ctx.Err())
		return waitResult{}
	}
}

func assertWorkerError(
	t *testing.T,
	runtime *runtime,
	workerID string,
	wantState WorkerState,
	wantErr error,
) {
	t.Helper()

	snapshot, err := runtime.Snapshot(workerID)
	if err != nil {
		t.Fatalf("snapshot worker %q: %v", workerID, err)
	}
	if snapshot.State != wantState {
		t.Fatalf("worker %q state = %s, want %s", workerID, snapshot.State, wantState)
	}
	if !errors.Is(snapshot.Err, wantErr) {
		t.Fatalf("worker %q error = %v, want errors.Is(_, %v)", workerID, snapshot.Err, wantErr)
	}
}
