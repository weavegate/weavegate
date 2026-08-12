package matchingsut

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/weavegate/weavegate/internal/fixture"
	internalsut "github.com/weavegate/weavegate/internal/sut"
	"github.com/weavegate/weavegate/internal/sut/gonative"
)

const seededRequestID int64 = 42

func TestAssignSequential(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	runner := fixture.NewMySQLFixture()
	t.Cleanup(func() {
		if err := runner.Teardown(context.Background()); err != nil {
			t.Errorf("cleanup matching SUT fixture: %v", err)
		}
	})
	db, err := runner.Provision(ctx, fixture.FixtureSpec{
		Image:      "mysql:8.4",
		Migrations: filepath.Join("..", "db", "migration"),
		Seed:       filepath.Join("..", "db", "seed.sql"),
	})
	if err != nil {
		t.Fatalf("provision matching SUT fixture: %v", err)
	}

	variants := []string{string(variantVulnerable), string(variantFixed)}
	for index, selected := range variants {
		if index > 0 {
			resetFixture(t, ctx, runner)
		}
		t.Run("sequential "+selected, func(t *testing.T) {
			testSequentialVariant(t, ctx, db, selected)
		})
	}

	resetFixture(t, ctx, runner)
	t.Run("rolls back a sync-point failure", func(t *testing.T) {
		testSyncPointRollback(t, ctx, db)
	})

	resetFixture(t, ctx, runner)
	t.Run("rejects a missing request", func(t *testing.T) {
		testMissingRequest(t, ctx, db)
	})

	resetFixture(t, ctx, runner)
	t.Run("rejects an inactive request", func(t *testing.T) {
		testInactiveRequest(t, ctx, db)
	})
}

func TestRegistryDefaultsAndValidation(t *testing.T) {
	t.Parallel()

	registry := NewRegistry(nil)
	if _, ok := registry.syncPoint.(NoopSyncPoint); !ok {
		t.Fatalf("nil sync-point default = %T, want NoopSyncPoint", registry.syncPoint)
	}

	valid := internalsut.SUTConfig{
		Variant: string(variantVulnerable),
		Params:  map[string]string{"request_id": "42"},
	}
	commands, err := registry.Commands(valid)
	if err != nil {
		t.Fatalf("build valid matching commands: %v", err)
	}
	if len(commands) != 1 || commands[CommandAssign] == nil {
		t.Fatalf("matching commands = %#v, want assign command", commands)
	}

	invalid := []internalsut.SUTConfig{
		{Variant: "unknown", Params: map[string]string{"request_id": "42"}},
		{Variant: string(variantFixed), Params: map[string]string{"request_id": "invalid"}},
		{Variant: string(variantFixed), Params: map[string]string{"request_id": "0"}},
	}
	for _, cfg := range invalid {
		if _, err := registry.Commands(cfg); err == nil {
			t.Fatalf("commands for config %#v returned nil error", cfg)
		}
	}
}

func testSequentialVariant(
	t *testing.T,
	ctx context.Context,
	db *fixture.DB,
	selected string,
) {
	t.Helper()

	recorder := &recordingSyncPoint{}
	adapter, handle := startMatchingAdapter(t, ctx, db, recorder, selected, seededRequestID)
	defer stopMatchingAdapter(t, adapter)

	firstWorker := selected + "-first"
	first := invokeAssignment(t, ctx, handle, firstWorker)
	if first.Err != nil {
		t.Fatalf("first %s assignment: %v", selected, first.Err)
	}
	assertWorkerPoints(t, recorder, firstWorker, []string{
		AfterReadRequest,
		BeforeInsertAssignment,
	})
	assertWorkflowCounts(t, ctx, db.SQL, workflowCounts{
		sessions:          1,
		assignments:       1,
		activeAssignments: 1,
	})

	secondWorker := selected + "-second"
	second := invokeAssignment(t, ctx, handle, secondWorker)
	if second.Err != nil {
		t.Fatalf("second %s assignment: %v", selected, second.Err)
	}
	assertWorkerPoints(t, recorder, secondWorker, []string{AfterReadRequest})
	counts := readWorkflowCounts(t, ctx, db.SQL)
	if counts != (workflowCounts{sessions: 1, assignments: 1, activeAssignments: 1}) {
		t.Fatalf("counts after sequential %s assignment = %+v, want 1/1/1", selected, counts)
	}

	t.Logf(
		"SUT_ASSIGN_SEQUENTIAL_RESULT variant=%s first_err=none second_err=none "+
			"sessions=%d assignments=%d active_assignments=%d "+
			"first_points=after_read_request,before_insert_assignment "+
			"second_points=after_read_request",
		selected,
		counts.sessions,
		counts.assignments,
		counts.activeAssignments,
	)
}

func testSyncPointRollback(
	t *testing.T,
	ctx context.Context,
	db *fixture.DB,
) {
	t.Helper()

	wantErr := errors.New("sync-point failed")
	recorder := &recordingSyncPoint{
		failPoint: BeforeInsertAssignment,
		failErr:   wantErr,
	}
	adapter, handle := startMatchingAdapter(
		t,
		ctx,
		db,
		recorder,
		string(variantVulnerable),
		seededRequestID,
	)
	defer stopMatchingAdapter(t, adapter)

	workerID := "rollback-worker"
	result := invokeAssignment(t, ctx, handle, workerID)
	if !errors.Is(result.Err, wantErr) {
		t.Fatalf("rollback result error = %v, want %v", result.Err, wantErr)
	}
	assertWorkerPoints(t, recorder, workerID, []string{
		AfterReadRequest,
		BeforeInsertAssignment,
	})
	counts := readWorkflowCounts(t, ctx, db.SQL)
	if counts != (workflowCounts{}) {
		t.Fatalf("counts after sync-point failure = %+v, want zero", counts)
	}

	t.Log(
		"SUT_ROLLBACK_RESULT variant=vulnerable " +
			"failed_point=before_insert_assignment sessions=0 assignments=0 err=propagated",
	)
}

func testMissingRequest(
	t *testing.T,
	ctx context.Context,
	db *fixture.DB,
) {
	t.Helper()

	recorder := &recordingSyncPoint{}
	adapter, handle := startMatchingAdapter(
		t,
		ctx,
		db,
		recorder,
		string(variantVulnerable),
		404,
	)
	defer stopMatchingAdapter(t, adapter)

	result := invokeAssignment(t, ctx, handle, "missing-worker")
	if !errors.Is(result.Err, ErrRequestNotFound) {
		t.Fatalf("missing request error = %v, want %v", result.Err, ErrRequestNotFound)
	}
	assertWorkerPoints(t, recorder, "missing-worker", nil)
	counts := readWorkflowCounts(t, ctx, db.SQL)
	if counts != (workflowCounts{}) {
		t.Fatalf("counts after missing request = %+v, want zero", counts)
	}

	t.Log("SUT_ASSIGN_ERROR_RESULT case=missing_request err=propagated sessions=0 assignments=0")
}

func testInactiveRequest(
	t *testing.T,
	ctx context.Context,
	db *fixture.DB,
) {
	t.Helper()

	if _, err := db.SQL.ExecContext(
		ctx,
		"UPDATE project_request SET status = 'INACTIVE' WHERE id = ?",
		seededRequestID,
	); err != nil {
		t.Fatalf("deactivate project request: %v", err)
	}

	recorder := &recordingSyncPoint{}
	adapter, handle := startMatchingAdapter(
		t,
		ctx,
		db,
		recorder,
		string(variantFixed),
		seededRequestID,
	)
	defer stopMatchingAdapter(t, adapter)

	result := invokeAssignment(t, ctx, handle, "inactive-worker")
	if !errors.Is(result.Err, ErrRequestInactive) {
		t.Fatalf("inactive request error = %v, want %v", result.Err, ErrRequestInactive)
	}
	assertWorkerPoints(t, recorder, "inactive-worker", nil)
	counts := readWorkflowCounts(t, ctx, db.SQL)
	if counts != (workflowCounts{}) {
		t.Fatalf("counts after inactive request = %+v, want zero", counts)
	}

	t.Log("SUT_ASSIGN_ERROR_RESULT case=inactive_request err=propagated sessions=0 assignments=0")
}

func startMatchingAdapter(
	t *testing.T,
	ctx context.Context,
	db *fixture.DB,
	syncPoint SyncPoint,
	selected string,
	requestID int64,
) (internalsut.Adapter, internalsut.Handle) {
	t.Helper()

	adapter := gonative.New(NewRegistry(syncPoint))
	handle, err := adapter.Start(ctx, internalsut.SUTConfig{
		Variant: selected,
		Params:  map[string]string{"request_id": fmt.Sprint(requestID)},
	}, db)
	if err != nil {
		t.Fatalf("start matching adapter: %v", err)
	}

	return adapter, handle
}

func stopMatchingAdapter(t *testing.T, adapter internalsut.Adapter) {
	t.Helper()

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := adapter.Stop(stopCtx); err != nil {
		t.Errorf("stop matching adapter: %v", err)
	}
}

func invokeAssignment(
	t *testing.T,
	ctx context.Context,
	handle internalsut.Handle,
	workerID string,
) internalsut.WorkerResult {
	t.Helper()

	results, err := handle.Invoke(ctx, workerID, CommandAssign)
	if err != nil {
		t.Fatalf("invoke assignment worker %q: %v", workerID, err)
	}
	select {
	case result, ok := <-results:
		if !ok {
			t.Fatalf("assignment worker %q result channel closed without a result", workerID)
		}
		select {
		case _, ok := <-results:
			if ok {
				t.Fatalf("assignment worker %q returned more than one result", workerID)
			}
		case <-ctx.Done():
			t.Fatalf("wait for assignment worker %q result close: %v", workerID, ctx.Err())
		}
		if result.WorkerID != workerID {
			t.Fatalf("assignment result worker ID = %q, want %q", result.WorkerID, workerID)
		}
		return result
	case <-ctx.Done():
		t.Fatalf("wait for assignment worker %q: %v", workerID, ctx.Err())
		return internalsut.WorkerResult{}
	}
}

type syncPointEvent struct {
	workerID string
	point    string
}

type recordingSyncPoint struct {
	mu sync.Mutex

	events    []syncPointEvent
	failPoint string
	failErr   error
}

func (s *recordingSyncPoint) Arrive(
	ctx context.Context,
	workerID string,
	point string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	s.events = append(s.events, syncPointEvent{workerID: workerID, point: point})
	fail := point == s.failPoint
	failErr := s.failErr
	s.mu.Unlock()

	if fail {
		return failErr
	}
	return nil
}

func (s *recordingSyncPoint) points(workerID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	var points []string
	for _, event := range s.events {
		if event.workerID == workerID {
			points = append(points, event.point)
		}
	}
	return points
}

func assertWorkerPoints(
	t *testing.T,
	recorder *recordingSyncPoint,
	workerID string,
	want []string,
) {
	t.Helper()

	got := recorder.points(workerID)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("worker %q sync-points = %#v, want %#v", workerID, got, want)
	}
}

type workflowCounts struct {
	sessions          int
	assignments       int
	activeAssignments int
}

func assertWorkflowCounts(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	want workflowCounts,
) {
	t.Helper()

	if got := readWorkflowCounts(t, ctx, db); got != want {
		t.Fatalf("matching workflow counts = %+v, want %+v", got, want)
	}
}

func readWorkflowCounts(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
) workflowCounts {
	t.Helper()

	return workflowCounts{
		sessions:          queryScalar(t, ctx, db, "SELECT COUNT(*) FROM matching_session"),
		assignments:       queryScalar(t, ctx, db, "SELECT COUNT(*) FROM assignment"),
		activeAssignments: queryScalar(t, ctx, db, "SELECT COUNT(*) FROM assignment WHERE status = 'ACTIVE'"),
	}
}

func queryScalar(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	query string,
) int {
	t.Helper()

	var count int
	if err := db.QueryRowContext(ctx, query).Scan(&count); err != nil {
		t.Fatalf("query matching workflow count: %v", err)
	}
	return count
}

func resetFixture(t *testing.T, ctx context.Context, runner fixture.Fixture) {
	t.Helper()

	if err := runner.Reset(ctx); err != nil {
		t.Fatalf("reset matching SUT fixture: %v", err)
	}
}
