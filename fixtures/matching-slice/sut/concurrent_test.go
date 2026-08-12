package matchingsut

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/weavegate/weavegate/internal/fixture"
	internalsut "github.com/weavegate/weavegate/internal/sut"
	"github.com/weavegate/weavegate/internal/syncpoint"
)

const (
	concurrentCoordinationTimeout = 15 * time.Second
	scheduleStepTimeout           = 5 * time.Second
	lockInferenceTimeout          = 250 * time.Millisecond
)

func TestAssignConcurrent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	runner := fixture.NewMySQLFixture()
	t.Cleanup(func() {
		if err := runner.Teardown(context.Background()); err != nil {
			t.Errorf("cleanup concurrent matching SUT fixture: %v", err)
		}
	})
	db, err := runner.Provision(ctx, fixture.FixtureSpec{
		Image:      "mysql:8.4",
		Migrations: filepath.Join("..", "db", "migration"),
		Seed:       filepath.Join("..", "db", "seed.sql"),
	})
	if err != nil {
		t.Fatalf("provision concurrent matching SUT fixture: %v", err)
	}

	cases := []concurrentCase{
		{
			variant:       string(variantVulnerable),
			wantCounts:    workflowCounts{sessions: 2, assignments: 2, activeAssignments: 2},
			wantDuplicate: true,
		},
		{
			variant:       string(variantFixed),
			wantCounts:    workflowCounts{sessions: 1, assignments: 1, activeAssignments: 1},
			wantDuplicate: false,
		},
	}

	for index, tc := range cases {
		if index > 0 {
			resetFixture(t, ctx, runner)
		}
		t.Run(tc.variant, func(t *testing.T) {
			testConcurrentVariant(t, ctx, db, tc)
		})
	}
}

type concurrentCase struct {
	variant       string
	wantCounts    workflowCounts
	wantDuplicate bool
}

func testConcurrentVariant(
	t *testing.T,
	ctx context.Context,
	db *fixture.DB,
	tc concurrentCase,
) {
	t.Helper()

	scenarioCtx, cancel := context.WithTimeout(ctx, concurrentCoordinationTimeout)
	defer cancel()

	runtime := syncpoint.New()
	defer runtime.Close()
	adapter, handle := startMatchingAdapter(
		t,
		scenarioCtx,
		db,
		runtime,
		tc.variant,
		seededRequestID,
	)
	defer stopMatchingAdapter(t, adapter)

	if waits := readCurrentInnoDBRowLockWaits(t, scenarioCtx, db.SQL); waits != 0 {
		t.Fatalf("current InnoDB row lock waits before %s scenario = %d, want 0", tc.variant, waits)
	}

	workerIDs := []string{"w1", "w2"}
	w1 := invokeRuntimeWorker(t, scenarioCtx, runtime, handle, workerIDs[0])
	waitForRuntimeArrival(t, scenarioCtx, runtime, workerIDs[0], AfterReadRequest)
	w2 := invokeRuntimeWorker(t, scenarioCtx, runtime, handle, workerIDs[1])

	var (
		results          []internalsut.WorkerResult
		w2AfterRead      string
		timeoutInferred  int
		terminalAtInsert string
	)
	if tc.variant == string(variantVulnerable) {
		results = runVulnerableSchedule(t, scenarioCtx, runtime, db.SQL, w1, w2)
		w2AfterRead = "arrived"
		timeoutInferred = 0
	} else {
		results = runFixedSchedule(t, scenarioCtx, runtime, db.SQL, w1, w2)
		w2AfterRead = "timeout"
		timeoutInferred = 1
		terminalAtInsert = "done"
	}

	if err := waitForInnoDBRowLockWaits(scenarioCtx, db.SQL, 0); err != nil {
		t.Fatalf("wait for InnoDB row lock cleanup: %v", err)
	}
	for _, result := range results {
		if result.Err != nil {
			t.Fatalf("concurrent %s worker %q: %v", tc.variant, result.WorkerID, result.Err)
		}
	}
	assertRuntimeWorkerIdentity(t, runtime, workerIDs, results)

	counts := readWorkflowCounts(t, scenarioCtx, db.SQL)
	if counts != tc.wantCounts {
		t.Fatalf("concurrent %s counts = %+v, want %+v", tc.variant, counts, tc.wantCounts)
	}
	duplicate := counts.activeAssignments > 1
	if duplicate != tc.wantDuplicate {
		t.Fatalf("concurrent %s duplicate = %t, want %t", tc.variant, duplicate, tc.wantDuplicate)
	}

	if tc.variant == string(variantVulnerable) {
		t.Logf(
			"SUT_SYNCPOINT_RESULT variant=vulnerable workers=2 errors=0 sessions=%d "+
				"assignments=%d active_assignments=%d duplicate=%t w2_after_read=%s "+
				"timeout_inferred=%d worker_identity=preserved",
			counts.sessions,
			counts.assignments,
			counts.activeAssignments,
			duplicate,
			w2AfterRead,
			timeoutInferred,
		)
		return
	}

	t.Logf(
		"SUT_SYNCPOINT_RESULT variant=fixed workers=2 errors=0 sessions=%d assignments=%d "+
			"active_assignments=%d duplicate=%t w2_after_read=%s terminal_before_insert=%s "+
			"timeout_inferred=%d worker_identity=preserved",
		counts.sessions,
		counts.assignments,
		counts.activeAssignments,
		duplicate,
		w2AfterRead,
		terminalAtInsert,
		timeoutInferred,
	)
}

func runVulnerableSchedule(
	t *testing.T,
	ctx context.Context,
	runtime syncpoint.Runtime,
	db *sql.DB,
	w1 <-chan runtimeWorkerResult,
	w2 <-chan runtimeWorkerResult,
) []internalsut.WorkerResult {
	t.Helper()

	waitForRuntimeArrival(t, ctx, runtime, "w2", AfterReadRequest)
	if waits := readCurrentInnoDBRowLockWaits(t, ctx, db); waits != 0 {
		t.Fatalf("vulnerable row lock waits before release = %d, want 0", waits)
	}
	releaseRuntimePoint(t, ctx, runtime, "w1", AfterReadRequest)
	releaseRuntimePoint(t, ctx, runtime, "w2", AfterReadRequest)
	waitForRuntimeArrival(t, ctx, runtime, "w1", BeforeInsertAssignment)
	waitForRuntimeArrival(t, ctx, runtime, "w2", BeforeInsertAssignment)

	releaseRuntimePoint(t, ctx, runtime, "w1", BeforeInsertAssignment)
	w1Result := awaitRuntimeWorkerResult(t, ctx, w1, "w1")
	releaseRuntimePoint(t, ctx, runtime, "w2", BeforeInsertAssignment)
	w2Result := awaitRuntimeWorkerResult(t, ctx, w2, "w2")

	return []internalsut.WorkerResult{w1Result, w2Result}
}

func runFixedSchedule(
	t *testing.T,
	ctx context.Context,
	runtime syncpoint.Runtime,
	db *sql.DB,
	w1 <-chan runtimeWorkerResult,
	w2 <-chan runtimeWorkerResult,
) []internalsut.WorkerResult {
	t.Helper()

	status, err := runtime.WaitArrive(ctx, "w2", AfterReadRequest, lockInferenceTimeout)
	if err != nil {
		t.Fatalf("wait for fixed w2 timeout inference: %v", err)
	}
	if status != syncpoint.ArriveStatusTimeout {
		t.Fatalf("fixed w2 wait status = %s, want timeout", status)
	}
	assertRuntimeState(t, runtime, "w2", syncpoint.WorkerStateDBBlocked, AfterReadRequest)
	if err := waitForInnoDBRowLockWaits(ctx, db, 1); err != nil {
		t.Fatalf("wait for fixed worker row lock: %v", err)
	}
	assertRuntimeWorkerPending(t, w2, "w2", "before row lock release")

	releaseRuntimePoint(t, ctx, runtime, "w1", AfterReadRequest)
	waitForRuntimeArrival(t, ctx, runtime, "w1", BeforeInsertAssignment)
	releaseRuntimePoint(t, ctx, runtime, "w1", BeforeInsertAssignment)
	w1Result := awaitRuntimeWorkerResult(t, ctx, w1, "w1")

	waitForRuntimeArrival(t, ctx, runtime, "w2", AfterReadRequest)
	releaseRuntimePoint(t, ctx, runtime, "w2", AfterReadRequest)
	w2Result := awaitRuntimeWorkerResult(t, ctx, w2, "w2")

	status, err = runtime.WaitArrive(ctx, "w2", BeforeInsertAssignment, scheduleStepTimeout)
	if err != nil {
		t.Fatalf("wait for fixed w2 terminal state before insert: %v", err)
	}
	if status != syncpoint.ArriveStatusDone {
		t.Fatalf("fixed w2 status before insert = %s, want done", status)
	}

	return []internalsut.WorkerResult{w1Result, w2Result}
}

type runtimeWorkerResult struct {
	result        internalsut.WorkerResult
	collectionErr error
}

func invokeRuntimeWorker(
	t *testing.T,
	ctx context.Context,
	runtime syncpoint.Runtime,
	handle internalsut.Handle,
	workerID string,
) <-chan runtimeWorkerResult {
	t.Helper()

	if err := runtime.Register(workerID); err != nil {
		t.Fatalf("register concurrent assignment worker %q: %v", workerID, err)
	}
	results, err := handle.Invoke(ctx, workerID, CommandAssign)
	if err != nil {
		t.Fatalf("invoke concurrent assignment worker %q: %v", workerID, err)
	}

	collected := make(chan runtimeWorkerResult, 1)
	go func() {
		result, ok := <-results
		if !ok {
			collected <- runtimeWorkerResult{
				collectionErr: fmt.Errorf("worker %q result channel closed without a result", workerID),
			}
			close(collected)
			return
		}
		if _, open := <-results; open {
			collected <- runtimeWorkerResult{
				result:        result,
				collectionErr: fmt.Errorf("worker %q returned more than one result", workerID),
			}
			close(collected)
			return
		}
		finishErr := runtime.Finish(result.WorkerID, result.Err)
		collected <- runtimeWorkerResult{result: result, collectionErr: finishErr}
		close(collected)
	}()

	return collected
}

func awaitRuntimeWorkerResult(
	t *testing.T,
	ctx context.Context,
	result <-chan runtimeWorkerResult,
	wantWorkerID string,
) internalsut.WorkerResult {
	t.Helper()

	select {
	case got, ok := <-result:
		if !ok {
			t.Fatalf("worker %q collected result channel closed without a result", wantWorkerID)
		}
		if got.collectionErr != nil {
			t.Fatalf("collect worker %q result: %v", wantWorkerID, got.collectionErr)
		}
		if got.result.WorkerID != wantWorkerID {
			t.Fatalf("assignment result worker ID = %q, want %q", got.result.WorkerID, wantWorkerID)
		}
		if _, open := <-result; open {
			t.Fatalf("worker %q returned more than one collected result", wantWorkerID)
		}
		return got.result
	case <-ctx.Done():
		t.Fatalf("wait for assignment worker %q: %v", wantWorkerID, ctx.Err())
		return internalsut.WorkerResult{}
	}
}

func assertRuntimeWorkerPending(
	t *testing.T,
	result <-chan runtimeWorkerResult,
	workerID string,
	phase string,
) {
	t.Helper()

	select {
	case got, open := <-result:
		t.Fatalf("worker %q completed %s: result=%+v open=%t", workerID, phase, got, open)
	default:
	}
}

func waitForRuntimeArrival(
	t *testing.T,
	ctx context.Context,
	runtime syncpoint.Runtime,
	workerID string,
	point string,
) {
	t.Helper()

	status, err := runtime.WaitArrive(ctx, workerID, point, scheduleStepTimeout)
	if err != nil {
		t.Fatalf("wait for worker %q at %q: %v", workerID, point, err)
	}
	if status != syncpoint.ArriveStatusArrived {
		t.Fatalf("worker %q status at %q = %s, want arrived", workerID, point, status)
	}
}

func releaseRuntimePoint(
	t *testing.T,
	ctx context.Context,
	runtime syncpoint.Runtime,
	workerID string,
	point string,
) {
	t.Helper()

	if err := runtime.Release(ctx, workerID, point); err != nil {
		t.Fatalf("release worker %q at %q: %v", workerID, point, err)
	}
}

func assertRuntimeState(
	t *testing.T,
	runtime syncpoint.Runtime,
	workerID string,
	wantState syncpoint.WorkerState,
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

func assertRuntimeWorkerIdentity(
	t *testing.T,
	runtime syncpoint.Runtime,
	wantWorkerIDs []string,
	results []internalsut.WorkerResult,
) {
	t.Helper()

	if len(results) != len(wantWorkerIDs) {
		t.Fatalf("worker results = %d, want %d", len(results), len(wantWorkerIDs))
	}
	for index, workerID := range wantWorkerIDs {
		if results[index].WorkerID != workerID {
			t.Fatalf("result %d worker ID = %q, want %q", index, results[index].WorkerID, workerID)
		}
		snapshot, err := runtime.Snapshot(workerID)
		if err != nil {
			t.Fatalf("snapshot worker %q: %v", workerID, err)
		}
		if snapshot.WorkerID != workerID || snapshot.State != syncpoint.WorkerStateDone {
			t.Fatalf("worker %q snapshot = %+v, want terminal identity-preserved done", workerID, snapshot)
		}
	}
}

func waitForInnoDBRowLockWaits(ctx context.Context, db *sql.DB, want int) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		got, err := currentInnoDBRowLockWaits(ctx, db)
		if err != nil {
			return err
		}
		if got == want {
			return nil
		}
		if got > want {
			return fmt.Errorf("Innodb_row_lock_current_waits = %d, want %d", got, want)
		}

		select {
		case <-ticker.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func readCurrentInnoDBRowLockWaits(t *testing.T, ctx context.Context, db *sql.DB) int {
	t.Helper()

	waits, err := currentInnoDBRowLockWaits(ctx, db)
	if err != nil {
		t.Fatalf("read Innodb_row_lock_current_waits: %v", err)
	}
	return waits
}

func currentInnoDBRowLockWaits(ctx context.Context, db *sql.DB) (int, error) {
	var name string
	var waits int
	if err := db.QueryRowContext(
		ctx,
		"SHOW GLOBAL STATUS LIKE 'Innodb_row_lock_current_waits'",
	).Scan(&name, &waits); err != nil {
		return 0, err
	}
	if name != "Innodb_row_lock_current_waits" {
		return 0, fmt.Errorf("unexpected InnoDB status variable %q", name)
	}
	return waits, nil
}
