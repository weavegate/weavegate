package matchingsut

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/weavegate/weavegate/internal/fixture"
	internalsut "github.com/weavegate/weavegate/internal/sut"
)

const concurrentCoordinationTimeout = 15 * time.Second

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
			wantRelease:   barrierAllArrived,
		},
		{
			variant:       string(variantFixed),
			wantCounts:    workflowCounts{sessions: 1, assignments: 1, activeAssignments: 1},
			wantDuplicate: false,
			wantRelease:   barrierDBBlocked,
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
	wantRelease   barrierReleaseReason
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

	barrier := newTestBarrier(BeforeInsertAssignment)
	adapter, handle := startMatchingAdapter(t, scenarioCtx, db, barrier, tc.variant, seededRequestID)
	defer stopMatchingAdapter(t, adapter)
	defer barrier.Release(barrierTestCleanup)

	if waits := readCurrentInnoDBRowLockWaits(t, scenarioCtx, db.SQL); waits != 0 {
		t.Fatalf("current InnoDB row lock waits before %s scenario = %d, want 0", tc.variant, waits)
	}

	workerIDs := []string{"w1", "w2"}
	resultChannels := make([]<-chan internalsut.WorkerResult, 0, len(workerIDs))
	resultChannels = append(resultChannels, invokeConcurrentWorker(t, scenarioCtx, handle, workerIDs[0]))
	if err := barrier.waitForArrivals(scenarioCtx, workerIDs[0]); err != nil {
		t.Fatalf("wait for first %s worker at barrier: %v", tc.variant, err)
	}
	resultChannels = append(resultChannels, invokeConcurrentWorker(t, scenarioCtx, handle, workerIDs[1]))

	switch tc.wantRelease {
	case barrierAllArrived:
		if err := barrier.waitForArrivals(scenarioCtx, workerIDs...); err != nil {
			t.Fatalf("wait for vulnerable workers at barrier: %v", err)
		}
		if waits := readCurrentInnoDBRowLockWaits(t, scenarioCtx, db.SQL); waits != 0 {
			t.Fatalf("vulnerable row lock waits before release = %d, want 0", waits)
		}
		barrier.Release(barrierAllArrived)
	case barrierDBBlocked:
		if err := waitForInnoDBRowLockWaits(scenarioCtx, db.SQL, 1); err != nil {
			t.Fatalf("wait for fixed worker row lock: %v", err)
		}
		observation := barrier.observation()
		if !reflect.DeepEqual(observation.arrivals, []string{workerIDs[0]}) {
			t.Fatalf("fixed arrivals before database release = %v, want [%s]", observation.arrivals, workerIDs[0])
		}
		select {
		case result, ok := <-resultChannels[1]:
			t.Fatalf("fixed worker completed before row lock release: result=%+v open=%t", result, ok)
		default:
		}
		barrier.Release(barrierDBBlocked)
	default:
		t.Fatalf("unsupported barrier release %q", tc.wantRelease)
	}

	results := make([]internalsut.WorkerResult, 0, len(workerIDs))
	for index, resultChannel := range resultChannels {
		results = append(
			results,
			awaitAssignmentResult(t, scenarioCtx, resultChannel, workerIDs[index]),
		)
	}
	for _, result := range results {
		if result.Err != nil {
			t.Fatalf("concurrent %s worker %q: %v", tc.variant, result.WorkerID, result.Err)
		}
	}

	if err := waitForInnoDBRowLockWaits(scenarioCtx, db.SQL, 0); err != nil {
		t.Fatalf("wait for InnoDB row lock cleanup: %v", err)
	}
	counts := readWorkflowCounts(t, scenarioCtx, db.SQL)
	if counts != tc.wantCounts {
		t.Fatalf("concurrent %s counts = %+v, want %+v", tc.variant, counts, tc.wantCounts)
	}
	duplicate := counts.activeAssignments > 1
	if duplicate != tc.wantDuplicate {
		t.Fatalf("concurrent %s duplicate = %t, want %t", tc.variant, duplicate, tc.wantDuplicate)
	}

	observation := barrier.observation()
	if observation.release != tc.wantRelease {
		t.Fatalf("concurrent %s barrier = %s, want %s", tc.variant, observation, tc.wantRelease)
	}
	wantReleased := 2
	if tc.wantRelease == barrierDBBlocked {
		wantReleased = 1
	}
	if len(observation.releasedIDs) != wantReleased {
		t.Fatalf("concurrent %s released worker IDs = %v, want %d", tc.variant, observation.releasedIDs, wantReleased)
	}
	if len(observation.arrivals) != wantReleased {
		t.Fatalf("concurrent %s barrier arrivals = %v, want %d", tc.variant, observation.arrivals, wantReleased)
	}
	assertConcurrentWorkerIdentity(t, workerIDs, results, observation)

	t.Logf(
		"SUT_ASSIGN_RESULT variant=%s workers=2 errors=0 sessions=%d assignments=%d "+
			"active_assignments=%d duplicate=%t barrier=%s worker_identity=preserved",
		tc.variant,
		counts.sessions,
		counts.assignments,
		counts.activeAssignments,
		duplicate,
		observation.release,
	)
}

func invokeConcurrentWorker(
	t *testing.T,
	ctx context.Context,
	handle internalsut.Handle,
	workerID string,
) <-chan internalsut.WorkerResult {
	t.Helper()

	results, err := handle.Invoke(ctx, workerID, CommandAssign)
	if err != nil {
		t.Fatalf("invoke concurrent assignment worker %q: %v", workerID, err)
	}
	return results
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

func awaitAssignmentResult(
	t *testing.T,
	ctx context.Context,
	results <-chan internalsut.WorkerResult,
	wantWorkerID string,
) internalsut.WorkerResult {
	t.Helper()

	select {
	case result, ok := <-results:
		if !ok {
			t.Fatalf("assignment worker %q result channel closed without a result", wantWorkerID)
		}
		if result.WorkerID != wantWorkerID {
			t.Fatalf("assignment result worker ID = %q, want %q", result.WorkerID, wantWorkerID)
		}
		if _, ok := <-results; ok {
			t.Fatalf("assignment worker %q returned more than one result", wantWorkerID)
		}
		return result
	case <-ctx.Done():
		t.Fatalf("wait for assignment worker %q: %v", wantWorkerID, ctx.Err())
		return internalsut.WorkerResult{}
	}
}

func assertConcurrentWorkerIdentity(
	t *testing.T,
	wantWorkerIDs []string,
	results []internalsut.WorkerResult,
	observation barrierObservation,
) {
	t.Helper()

	want := append([]string(nil), wantWorkerIDs...)
	sort.Strings(want)
	gotResults := make([]string, 0, len(results))
	for _, result := range results {
		gotResults = append(gotResults, result.WorkerID)
	}
	sort.Strings(gotResults)
	if !reflect.DeepEqual(gotResults, want) {
		t.Fatalf("concurrent result worker IDs = %v, want %v", gotResults, want)
	}

	valid := make(map[string]struct{}, len(wantWorkerIDs))
	seen := make(map[string]struct{}, len(wantWorkerIDs))
	for _, workerID := range wantWorkerIDs {
		valid[workerID] = struct{}{}
	}
	for _, event := range observation.events {
		if _, ok := valid[event.workerID]; !ok {
			t.Fatalf("unexpected sync-point worker ID %q in %s", event.workerID, observation)
		}
		seen[event.workerID] = struct{}{}
	}
	if len(seen) != len(wantWorkerIDs) {
		t.Fatalf("sync-point worker IDs in %s, want both %v", observation, wantWorkerIDs)
	}

	for _, workerID := range observation.releasedIDs {
		if _, ok := valid[workerID]; !ok {
			t.Fatalf("unexpected released worker ID %q in %s", workerID, observation)
		}
	}

	if observation.release == barrierAllArrived {
		gotArrivals := append([]string(nil), observation.releasedIDs...)
		sort.Strings(gotArrivals)
		if !reflect.DeepEqual(gotArrivals, want) {
			t.Fatalf("all-arrived worker IDs = %v, want %v", gotArrivals, want)
		}
	}
}
