package matchingsut

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/weavegate/weavegate/internal/fixture"
	"github.com/weavegate/weavegate/internal/oracle"
	internalsut "github.com/weavegate/weavegate/internal/sut"
)

const (
	baselineIterations  = 100
	baselineTestTimeout = 14 * time.Minute
)

type baselineEvidence struct {
	detections   int
	workerErrors int
	deadlocks    int
	elapsed      time.Duration
}

func TestBaselineComparison(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), baselineTestTimeout)
	defer cancel()

	runner := fixture.NewMySQLFixture()
	t.Cleanup(func() {
		if err := runner.Teardown(context.Background()); err != nil {
			t.Errorf("cleanup matching baseline fixture: %v", err)
		}
	})
	prepared, err := fixture.Prepare(fixture.FixtureSpec{
		Image:      "mysql:8.4",
		Migrations: filepath.Join("..", "db", "migration"),
		Seed:       filepath.Join("..", "db", "seed.sql"),
	})
	if err != nil {
		t.Fatalf("prepare matching baseline fixture: %v", err)
	}
	db, err := runner.Provision(ctx, prepared)
	if err != nil {
		t.Fatalf("provision matching baseline fixture: %v", err)
	}

	plain := measureMatchingBaseline(t, ctx, runner, db, nil)
	t.Logf(
		"MATCHING_BASELINE_RESULT arm=plain variant=vulnerable iterations=100 "+
			"detections=%d worker_errors=%d deadlocks=%d elapsed=%s",
		plain.detections,
		plain.workerErrors,
		plain.deadlocks,
		plain.elapsed,
	)
}

func measureMatchingBaseline(
	t *testing.T,
	ctx context.Context,
	runner fixture.Fixture,
	db *fixture.DB,
	syncPoint SyncPoint,
) baselineEvidence {
	t.Helper()

	evaluator, err := newMatchingInvariantOracleSet()
	if err != nil {
		t.Fatalf("create matching baseline Oracle set: %v", err)
	}
	evidence := baselineEvidence{}
	startedAt := time.Now()
	for iteration := 0; iteration < baselineIterations; iteration++ {
		resetFixture(t, ctx, runner)
		adapter, handle := startMatchingAdapter(
			t,
			ctx,
			db,
			syncPoint,
			string(variantVulnerable),
			seededRequestID,
		)
		results := invokeAssignmentsConcurrently(t, ctx, handle, "w1", "w2")
		stopMatchingAdapter(t, adapter)

		for _, result := range results {
			if result.Err == nil {
				continue
			}
			var mysqlErr *mysqldriver.MySQLError
			if errors.As(result.Err, &mysqlErr) && mysqlErr.Number == 1213 {
				evidence.deadlocks++
			} else {
				evidence.workerErrors++
			}
		}

		evaluation, err := evaluator.Evaluate(ctx, db.SQL, oracle.RunContext{})
		if err != nil {
			t.Fatalf("evaluate matching baseline iteration %d: %v", iteration+1, err)
		}
		if len(evaluation.Results) != 1 || evaluation.Results[0].OracleID != matchingOracleID {
			t.Fatalf("matching baseline iteration %d evaluation = %+v, want %q", iteration+1, evaluation, matchingOracleID)
		}
		if len(evaluation.Results[0].Violations) > 0 {
			evidence.detections++
		}
	}
	evidence.elapsed = time.Since(startedAt)
	return evidence
}

func invokeAssignmentsConcurrently(
	t *testing.T,
	ctx context.Context,
	handle internalsut.Handle,
	workerIDs ...string,
) []internalsut.WorkerResult {
	t.Helper()

	channels := make([]<-chan internalsut.WorkerResult, len(workerIDs))
	for index, workerID := range workerIDs {
		results, err := handle.Invoke(ctx, workerID, CommandAssign)
		if err != nil {
			t.Fatalf("invoke baseline assignment worker %q: %v", workerID, err)
		}
		channels[index] = results
	}

	results := make([]internalsut.WorkerResult, len(workerIDs))
	for index, workerID := range workerIDs {
		select {
		case result, ok := <-channels[index]:
			if !ok {
				t.Fatalf("baseline assignment worker %q result channel closed without a result", workerID)
			}
			if result.WorkerID != workerID {
				t.Fatalf("baseline assignment result worker ID = %q, want %q", result.WorkerID, workerID)
			}
			results[index] = result
		case <-ctx.Done():
			t.Fatalf("wait for baseline assignment worker %q: %v", workerID, ctx.Err())
		}
	}
	return results
}
