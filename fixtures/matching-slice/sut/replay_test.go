package matchingsut

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/weavegate/weavegate/internal/fixture"
	"github.com/weavegate/weavegate/internal/oracle"
	"github.com/weavegate/weavegate/internal/orchestrator"
	"github.com/weavegate/weavegate/internal/scenario"
	internalsut "github.com/weavegate/weavegate/internal/sut"
	"github.com/weavegate/weavegate/internal/sut/gonative"
	"github.com/weavegate/weavegate/internal/syncpoint"
)

const (
	matchingReplayRepeat       = 20
	matchingReplayTestTimeout  = 14 * time.Minute
	replayRunTimeout           = 15 * time.Second
	replayStepTimeout          = 5 * time.Second
	replayLockInferenceTimeout = 250 * time.Millisecond
	replayLockObservationWait  = 2 * time.Second
	replayLockObservationPoll  = 10 * time.Millisecond
	replayStopTimeout          = 5 * time.Second
)

func TestReplayConcurrentAssign(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), matchingReplayTestTimeout)
	defer cancel()

	runner := fixture.NewMySQLFixture()
	t.Cleanup(func() {
		if err := runner.Teardown(context.Background()); err != nil {
			t.Errorf("cleanup matching replay fixture: %v", err)
		}
	})
	db, err := runner.Provision(ctx, fixture.FixtureSpec{
		Image:      "mysql:8.4",
		Migrations: filepath.Join("..", "db", "migration"),
		Seed:       filepath.Join("..", "db", "seed.sql"),
	})
	if err != nil {
		t.Fatalf("provision matching replay fixture: %v", err)
	}

	saved, err := scenario.LoadScheduleFile(
		filepath.Join("..", "schedules", "concurrent-assign.json"),
	)
	if err != nil {
		t.Fatalf("load matching replay schedule: %v", err)
	}

	startedAt := time.Now()
	vulnerable := replayMatchingVariant(
		t,
		ctx,
		runner,
		db,
		saved,
		string(variantVulnerable),
	)
	fixed := replayMatchingVariant(
		t,
		ctx,
		runner,
		db,
		saved,
		string(variantFixed),
	)
	elapsed := time.Since(startedAt)

	t.Logf(
		"MATCHING_REPLAY_RESULT schedule=sch_ba00582f9632 variant=vulnerable repeat=20 "+
			"duplicate_runs=%d blocked_runs=%d sessions=2 assignments=2 active_assignments=2 "+
			"worker_errors=%d deadlocks=%d flaky=%t",
		vulnerable.duplicateRuns,
		vulnerable.blockedRuns,
		vulnerable.workerErrors,
		vulnerable.deadlocks,
		vulnerable.flaky,
	)
	t.Logf(
		"MATCHING_REPLAY_RESULT schedule=sch_ba00582f9632 variant=fixed repeat=20 "+
			"pass_runs=%d blocked_runs=%d lock_wait_runs=%d sessions=1 assignments=1 "+
			"active_assignments=1 worker_errors=%d deadlocks=%d flaky=%t",
		fixed.passRuns,
		fixed.blockedRuns,
		fixed.lockWaitRuns,
		fixed.workerErrors,
		fixed.deadlocks,
		fixed.flaky,
	)
	t.Logf(
		"MATCHING_REPLAY_TIMING schedule=sch_ba00582f9632 runs=40 elapsed=%s average=%s",
		elapsed,
		elapsed/(2*matchingReplayRepeat),
	)
}

type matchingReplayEvidence struct {
	passRuns      int
	duplicateRuns int
	blockedRuns   int
	lockWaitRuns  int
	workerErrors  int
	deadlocks     int
	flaky         bool
}

func replayMatchingVariant(
	t *testing.T,
	ctx context.Context,
	runner fixture.Fixture,
	db *fixture.DB,
	saved scenario.Schedule,
	selected string,
) matchingReplayEvidence {
	t.Helper()

	evidence := matchingReplayEvidence{}
	onEvent := func(event orchestrator.Event) error {
		if selected != string(variantFixed) || event.Kind != orchestrator.EventPointTimeout {
			return nil
		}
		if event.Status != orchestrator.ControlStatusTimeoutInferred ||
			event.FailureClass != orchestrator.WorkerFailureNone {
			return fmt.Errorf("fixed timeout event has mixed taxonomy: %+v", event)
		}

		waits, err := waitForInnoDBRowLockWaitEvidence(
			ctx,
			replayLockObservationWait,
			replayLockObservationPoll,
			func(sampleCtx context.Context) (int, error) {
				return currentInnoDBRowLockWaits(sampleCtx, db.SQL)
			},
		)
		if err != nil {
			return fmt.Errorf("observe fixed InnoDB row lock wait: %w", err)
		}
		if waits < 1 {
			return fmt.Errorf(
				"observe fixed InnoDB row lock wait at step %d: got %d, want at least 1",
				event.Step,
				waits,
			)
		}
		evidence.lockWaitRuns++
		return nil
	}

	executor, err := orchestrator.New(orchestrator.Config{
		Fixture:    runner,
		DB:         db,
		NewRuntime: syncpoint.New,
		NewAdapter: func(client syncpoint.Client) internalsut.Adapter {
			return gonative.New(NewRegistry(client))
		},
		BlockInferenceTimeout: replayLockInferenceTimeout,
		StepTimeout:           replayStepTimeout,
		RunTimeout:            replayRunTimeout,
		StopTimeout:           replayStopTimeout,
		OnEvent:               onEvent,
	})
	if err != nil {
		t.Fatalf("create %s replay orchestrator: %v", selected, err)
	}

	value := scenario.Scenario{
		Name: "matching-concurrent-assign",
		Workers: []scenario.Worker{
			{ID: "w1", Command: CommandAssign},
			{ID: "w2", Command: CommandAssign},
		},
		SyncPoints: []string{AfterReadRequest, BeforeInsertAssignment},
		SUTConfig: internalsut.SUTConfig{
			Variant: selected,
			Params:  map[string]string{"request_id": fmt.Sprint(seededRequestID)},
		},
	}

	replay, err := executor.Replay(
		ctx,
		value,
		saved,
		matchingReplayRepeat,
		matchingCountEvaluator(db),
	)
	if err != nil {
		t.Fatalf("replay matching %s variant: %v", selected, err)
	}
	evidence.flaky = replay.Flaky
	if replay.Flaky {
		t.Fatalf(
			"matching %s replay is flaky: fingerprints=%d mismatch_runs=%v",
			selected,
			len(replay.Fingerprints),
			replay.MismatchRuns,
		)
	}
	if len(replay.Fingerprints) != 1 {
		t.Fatalf("matching %s fingerprints = %d, want 1", selected, len(replay.Fingerprints))
	}
	if len(replay.Runs) != matchingReplayRepeat {
		t.Fatalf("matching %s runs = %d, want %d", selected, len(replay.Runs), matchingReplayRepeat)
	}

	wantCounts := workflowCounts{sessions: 2, assignments: 2, activeAssignments: 2}
	wantViolations := 1
	if selected == string(variantFixed) {
		wantCounts = workflowCounts{sessions: 1, assignments: 1, activeAssignments: 1}
		wantViolations = 0
	}

	for index, run := range replay.Runs {
		runNumber := index + 1
		if len(run.Evaluation.Results) != 1 ||
			run.Evaluation.Results[0].OracleID != "matching-counts" {
			t.Fatalf(
				"matching %s run %d evaluation = %+v, want matching-counts result",
				selected,
				runNumber,
				run.Evaluation,
			)
		}
		if got := len(run.Evaluation.Results[0].Violations); got != wantViolations {
			t.Fatalf(
				"matching %s run %d violations = %d, want %d",
				selected,
				runNumber,
				got,
				wantViolations,
			)
		}

		timeoutCount := 0
		terminalSkipped := false
		for _, event := range run.Trace {
			switch event.Kind {
			case orchestrator.EventPointTimeout:
				timeoutCount++
			case orchestrator.EventStepTerminalSkipped:
				if event.Step == 3 && event.Worker == "w2" &&
					event.Point == BeforeInsertAssignment {
					terminalSkipped = true
				}
			}
		}
		if timeoutCount > 0 {
			evidence.blockedRuns++
		}

		for workerIndex, terminal := range run.Terminals {
			if terminal.FailureClass == orchestrator.WorkerFailureMySQLDeadlock {
				evidence.deadlocks++
			} else if terminal.FailureClass != orchestrator.WorkerFailureNone {
				evidence.workerErrors++
			}
			if run.Workers[workerIndex].Err != nil {
				t.Fatalf(
					"matching %s run %d worker %q: %v",
					selected,
					runNumber,
					terminal.Worker,
					run.Workers[workerIndex].Err,
				)
			}
		}

		if selected == string(variantVulnerable) {
			if timeoutCount != 0 {
				t.Fatalf("vulnerable run %d timeout events = %d, want 0", runNumber, timeoutCount)
			}
			evidence.duplicateRuns++
			continue
		}

		if timeoutCount != 1 {
			t.Fatalf("fixed run %d timeout events = %d, want 1", runNumber, timeoutCount)
		}
		if !terminalSkipped {
			t.Fatalf("fixed run %d lacks terminal skip for w2 before insert", runNumber)
		}
		evidence.passRuns++
	}

	if selected == string(variantVulnerable) {
		if evidence.duplicateRuns != matchingReplayRepeat || evidence.blockedRuns != 0 {
			t.Fatalf("vulnerable replay evidence = %+v", evidence)
		}
	} else if evidence.passRuns != matchingReplayRepeat ||
		evidence.blockedRuns != matchingReplayRepeat ||
		evidence.lockWaitRuns != matchingReplayRepeat {
		t.Fatalf("fixed replay evidence = %+v", evidence)
	}
	if evidence.workerErrors != 0 || evidence.deadlocks != 0 {
		t.Fatalf("matching %s terminal failures = %+v", selected, evidence)
	}

	counts, err := readWorkflowCountsResult(ctx, db.SQL)
	if err != nil {
		t.Fatalf("read final matching %s counts: %v", selected, err)
	}
	if counts != wantCounts {
		t.Fatalf("final matching %s counts = %+v, want %+v", selected, counts, wantCounts)
	}
	return evidence
}

func TestWaitForInnoDBRowLockWaitEvidenceRetriesUntilVisible(t *testing.T) {
	attempts := 0
	waits, err := waitForInnoDBRowLockWaitEvidence(
		context.Background(),
		time.Second,
		time.Millisecond,
		func(context.Context) (int, error) {
			attempts++
			if attempts < 3 {
				return 0, nil
			}
			return 1, nil
		},
	)
	if err != nil {
		t.Fatalf("wait for delayed row-lock evidence: %v", err)
	}
	if waits != 1 || attempts != 3 {
		t.Fatalf("row-lock evidence waits=%d attempts=%d, want 1/3", waits, attempts)
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = waitForInnoDBRowLockWaitEvidence(
		canceledCtx,
		time.Second,
		time.Millisecond,
		func(context.Context) (int, error) { return 0, nil },
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled row-lock evidence error = %v, want context canceled", err)
	}
}

func waitForInnoDBRowLockWaitEvidence(
	ctx context.Context,
	timeout time.Duration,
	pollInterval time.Duration,
	sample func(context.Context) (int, error),
) (int, error) {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	last := 0
	for {
		waits, err := sample(waitCtx)
		if err != nil {
			return last, err
		}
		last = waits
		if waits >= 1 {
			return waits, nil
		}

		select {
		case <-waitCtx.Done():
			return last, fmt.Errorf("row-lock wait remained %d: %w", last, waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func matchingCountEvaluator(db *fixture.DB) oracle.Evaluator {
	return oracle.EvaluatorFunc(func(
		ctx context.Context,
		_ oracle.DB,
		_ oracle.RunContext,
	) (oracle.Evaluation, error) {
		if db == nil || db.SQL == nil {
			return oracle.Evaluation{}, fmt.Errorf("evaluate matching counts: database is required")
		}
		counts, err := readWorkflowCountsResult(ctx, db.SQL)
		if err != nil {
			return oracle.Evaluation{}, fmt.Errorf("evaluate matching counts: %w", err)
		}

		violations := []oracle.Violation{}
		if counts.activeAssignments > 1 {
			violations = append(violations, oracle.Violation{
				OracleID: "matching-counts",
				Kind:     oracle.KindAssertion,
				Rows: []oracle.Row{{
					"sessions":           int64(counts.sessions),
					"assignments":        int64(counts.assignments),
					"active_assignments": int64(counts.activeAssignments),
				}},
			})
		}
		return oracle.NewEvaluation(oracle.OracleResult{
			OracleID:   "matching-counts",
			Violations: violations,
		})
	})
}

func readWorkflowCountsResult(ctx context.Context, db *sql.DB) (workflowCounts, error) {
	queries := []string{
		"SELECT COUNT(*) FROM matching_session",
		"SELECT COUNT(*) FROM assignment",
		"SELECT COUNT(*) FROM assignment WHERE status = 'ACTIVE'",
	}
	values := make([]int, len(queries))
	for index, query := range queries {
		if err := db.QueryRowContext(ctx, query).Scan(&values[index]); err != nil {
			return workflowCounts{}, fmt.Errorf("query matching workflow count %d: %w", index, err)
		}
	}
	return workflowCounts{
		sessions:          values[0],
		assignments:       values[1],
		activeAssignments: values[2],
	}, nil
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
