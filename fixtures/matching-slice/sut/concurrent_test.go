package matchingsut

import (
	"context"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/weavegate/weavegate/internal/fixture"
	internalsut "github.com/weavegate/weavegate/internal/sut"
)

const concurrentBarrierTimeout = 2 * time.Second

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
			wantReleased:  2,
			wantArrivals:  2,
		},
		{
			variant:       string(variantFixed),
			wantCounts:    workflowCounts{sessions: 1, assignments: 1, activeAssignments: 1},
			wantDuplicate: false,
			wantRelease:   barrierTimeout,
			wantReleased:  1,
			wantArrivals:  1,
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
	wantReleased  int
	wantArrivals  int
}

func testConcurrentVariant(
	t *testing.T,
	ctx context.Context,
	db *fixture.DB,
	tc concurrentCase,
) {
	t.Helper()

	barrier := newTestBarrier(BeforeInsertAssignment, 2, concurrentBarrierTimeout)
	adapter, handle := startMatchingAdapter(t, ctx, db, barrier, tc.variant, seededRequestID)
	defer stopMatchingAdapter(t, adapter)

	workerIDs := []string{"w1", "w2"}
	resultChannels := make([]<-chan internalsut.WorkerResult, 0, len(workerIDs))
	for _, workerID := range workerIDs {
		results, err := handle.Invoke(ctx, workerID, CommandAssign)
		if err != nil {
			t.Fatalf("invoke concurrent assignment worker %q: %v", workerID, err)
		}
		resultChannels = append(resultChannels, results)
	}

	results := make([]internalsut.WorkerResult, 0, len(workerIDs))
	for index, resultChannel := range resultChannels {
		results = append(
			results,
			awaitAssignmentResult(t, ctx, resultChannel, workerIDs[index]),
		)
	}
	for _, result := range results {
		if result.Err != nil {
			t.Fatalf("concurrent %s worker %q: %v", tc.variant, result.WorkerID, result.Err)
		}
	}

	counts := readWorkflowCounts(t, ctx, db.SQL)
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
	if len(observation.releasedIDs) != tc.wantReleased {
		t.Fatalf(
			"concurrent %s released worker IDs = %v, want %d",
			tc.variant,
			observation.releasedIDs,
			tc.wantReleased,
		)
	}
	if len(observation.arrivals) != tc.wantArrivals {
		t.Fatalf(
			"concurrent %s barrier arrivals = %v, want %d",
			tc.variant,
			observation.arrivals,
			tc.wantArrivals,
		)
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
