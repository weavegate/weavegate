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
	"github.com/weavegate/weavegate/internal/scenario"
	internalsut "github.com/weavegate/weavegate/internal/sut"
)

const (
	baselineIterations  = 100
	baselineTestTimeout = 5 * time.Minute
	baselineHintedDelay = 2 * time.Millisecond
)

var baselineStaggers = []time.Duration{
	2 * time.Millisecond,
	20 * time.Millisecond,
	100 * time.Millisecond,
}

type launchMode int

const (
	launchConcurrent launchMode = iota
	launchStaggered
	launchSerial
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

	plain := measureMatchingBaseline(t, ctx, runner, db, nil, launchConcurrent, 0)
	t.Logf(
		"MATCHING_BASELINE_RESULT arm=plain variant=vulnerable iterations=100 launch=concurrent "+
			"detections=%d worker_errors=%d deadlocks=%d elapsed=%s",
		plain.detections,
		plain.workerErrors,
		plain.deadlocks,
		plain.elapsed,
	)
	requireCleanBaselineArm(t, "plain", plain)

	hinted := measureMatchingBaseline(t, ctx, runner, db, delayingSyncPoint{
		point: BeforeInsertAssignment,
		delay: baselineHintedDelay,
	}, launchConcurrent, 0)
	t.Logf(
		"MATCHING_BASELINE_RESULT arm=hinted_delay variant=vulnerable iterations=100 "+
			"launch=concurrent delay_ms=2 delay_point=before_insert_assignment detections=%d "+
			"worker_errors=%d deadlocks=%d elapsed=%s",
		hinted.detections,
		hinted.workerErrors,
		hinted.deadlocks,
		hinted.elapsed,
	)
	requireCleanBaselineArm(t, "hinted_delay", hinted)

	staggered := make([]baselineEvidence, len(baselineStaggers))
	for index, stagger := range baselineStaggers {
		staggered[index] = measureMatchingBaseline(t, ctx, runner, db, nil, launchStaggered, stagger)
		t.Logf(
			"MATCHING_BASELINE_RESULT arm=staggered_launch variant=vulnerable iterations=100 "+
				"launch=staggered stagger_ms=%d detections=%d worker_errors=%d deadlocks=%d elapsed=%s",
			stagger.Milliseconds(),
			staggered[index].detections,
			staggered[index].workerErrors,
			staggered[index].deadlocks,
			staggered[index].elapsed,
		)
		requireCleanBaselineArm(t, "staggered_launch_"+stagger.String(), staggered[index])
	}

	serial := measureMatchingBaseline(t, ctx, runner, db, nil, launchSerial, 0)
	t.Logf(
		"MATCHING_BASELINE_RESULT arm=control_serial variant=vulnerable iterations=100 "+
			"launch=serial detections=%d worker_errors=%d deadlocks=%d elapsed=%s",
		serial.detections,
		serial.workerErrors,
		serial.deadlocks,
		serial.elapsed,
	)
	requireCleanBaselineArm(t, "control_serial", serial)

	saved, err := scenario.LoadScheduleFile(
		filepath.Join("..", "schedules", "concurrent-assign.json"),
	)
	if err != nil {
		t.Fatalf("load matching baseline replay schedule: %v", err)
	}
	replay := replayMatchingVariant(
		t,
		ctx,
		runner,
		db,
		saved,
		string(variantVulnerable),
	)
	t.Logf(
		"MATCHING_BASELINE_COMPARE baseline_plain=%d/100 baseline_hinted=%d/100 "+
			"baseline_staggered_2ms=%d/100 baseline_staggered_20ms=%d/100 "+
			"baseline_staggered_100ms=%d/100 control_serial=%d/100 "+
			"schedule_replay=%d/20 schedule=sch_ba00582f9632 image=mysql:8.4 "+
			"same_fixture=true replayable=schedule_only",
		plain.detections,
		hinted.detections,
		staggered[0].detections,
		staggered[1].detections,
		staggered[2].detections,
		serial.detections,
		replay.violationRuns,
	)
}

func requireCleanBaselineArm(t *testing.T, arm string, evidence baselineEvidence) {
	t.Helper()

	if evidence.workerErrors != 0 || evidence.deadlocks != 0 {
		t.Errorf(
			"matching baseline arm %q recorded terminal failures: worker_errors=%d deadlocks=%d",
			arm,
			evidence.workerErrors,
			evidence.deadlocks,
		)
	}
}

type delayingSyncPoint struct {
	point string
	delay time.Duration
}

func (s delayingSyncPoint) Arrive(ctx context.Context, _ string, point string) error {
	if point != s.point {
		return ctx.Err()
	}

	timer := time.NewTimer(s.delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func measureMatchingBaseline(
	t *testing.T,
	ctx context.Context,
	runner fixture.Fixture,
	db *fixture.DB,
	syncPoint SyncPoint,
	mode launchMode,
	stagger time.Duration,
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
		results := invokeAssignments(t, ctx, handle, mode, stagger, "w1", "w2")
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

func invokeAssignments(
	t *testing.T,
	ctx context.Context,
	handle internalsut.Handle,
	mode launchMode,
	stagger time.Duration,
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
		if mode == launchSerial {
			return append(
				[]internalsut.WorkerResult{collectAssignmentResult(t, ctx, channels[index], workerID)},
				invokeAssignments(t, ctx, handle, launchSerial, 0, workerIDs[index+1:]...)...,
			)
		}
		if mode == launchStaggered && index < len(workerIDs)-1 {
			timer := time.NewTimer(stagger)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				t.Fatalf("stagger baseline assignment workers: %v", ctx.Err())
			}
		}
	}

	results := make([]internalsut.WorkerResult, len(workerIDs))
	for index, workerID := range workerIDs {
		results[index] = collectAssignmentResult(t, ctx, channels[index], workerID)
	}
	return results
}

func collectAssignmentResult(
	t *testing.T,
	ctx context.Context,
	results <-chan internalsut.WorkerResult,
	workerID string,
) internalsut.WorkerResult {
	t.Helper()

	select {
	case result, ok := <-results:
		if !ok {
			t.Fatalf("baseline assignment worker %q result channel closed without a result", workerID)
		}
		if result.WorkerID != workerID {
			t.Fatalf("baseline assignment result worker ID = %q, want %q", result.WorkerID, workerID)
		}
		return result
	case <-ctx.Done():
		t.Fatalf("wait for baseline assignment worker %q: %v", workerID, ctx.Err())
		return internalsut.WorkerResult{}
	}
}
