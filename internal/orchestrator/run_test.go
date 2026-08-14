package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/weavegate/weavegate/internal/fixture"
	"github.com/weavegate/weavegate/internal/scenario"
	"github.com/weavegate/weavegate/internal/sut"
	"github.com/weavegate/weavegate/internal/syncpoint"
)

const (
	testBlockTimeout = 50 * time.Millisecond
	testStepTimeout  = 500 * time.Millisecond
	testRunTimeout   = 3 * time.Second
	testStopTimeout  = 500 * time.Millisecond
)

func TestRunSavedSchedule(t *testing.T) {
	fixtureRunner := &recordingFixture{}
	runtime := newRuntimeProbe()
	var adapter *scriptedAdapter

	orchestrator := newTestOrchestrator(t, Config{
		Fixture:    fixtureRunner,
		DB:         &fixture.DB{},
		NewRuntime: func() syncpoint.Runtime { return runtime },
		NewAdapter: func(client syncpoint.Client) sut.Adapter {
			adapter = newScriptedAdapter(client)
			adapter.keepResultsOpen = true
			return adapter
		},
		BlockInferenceTimeout: testBlockTimeout,
		StepTimeout:           testStepTimeout,
		RunTimeout:            testRunTimeout,
		StopTimeout:           testStopTimeout,
	})

	observerCalled := false
	result, err := orchestrator.Run(
		context.Background(),
		matchingScenario(),
		matchingSchedule(t),
		func(_ context.Context, _ *fixture.DB, result RunResult) (string, error) {
			observerCalled = true
			if adapter.stopped.Load() {
				t.Fatal("adapter stopped before observer")
			}
			if len(result.Workers) != 2 {
				t.Fatalf("observer workers = %d, want 2", len(result.Workers))
			}
			result.Workers[0].WorkerID = "observer_mutation"
			return "sessions=1;assignments=1;active=1;duplicate=false", nil
		},
	)
	if err != nil {
		t.Fatalf("run saved schedule: %v", err)
	}
	if !observerCalled {
		t.Fatal("observer was not called")
	}
	if result.ScheduleID != "sch_ba00582f9632" || result.Steps != 4 {
		t.Fatalf("run identity = %q/%d, want sch_ba00582f9632/4", result.ScheduleID, result.Steps)
	}
	if result.Timeouts != 1 {
		t.Fatalf("run timeouts = %d, want 1", result.Timeouts)
	}
	if result.PendingResolved != 1 {
		t.Fatalf("pending resolved = %d, want 1", result.PendingResolved)
	}
	if len(result.Workers) != 2 {
		t.Fatalf("run workers = %d, want 2", len(result.Workers))
	}
	for index, workerID := range []string{"w1", "w2"} {
		if result.Workers[index].WorkerID != workerID {
			t.Fatalf("worker[%d] = %q, want %q", index, result.Workers[index].WorkerID, workerID)
		}
		if result.Workers[index].Err != nil {
			t.Fatalf("worker[%d] error = %v, want nil", index, result.Workers[index].Err)
		}
	}
	if result.Fingerprint != "sessions=1;assignments=1;active=1;duplicate=false" {
		t.Fatalf("run fingerprint = %q", result.Fingerprint)
	}
	if result.Elapsed <= 0 {
		t.Fatalf("run elapsed = %s, want positive", result.Elapsed)
	}
	if fixtureRunner.resetCalls != 1 {
		t.Fatalf("fixture resets = %d, want 1", fixtureRunner.resetCalls)
	}
	if adapter.stopCalls.Load() != 1 {
		t.Fatalf("adapter stops = %d, want 1", adapter.stopCalls.Load())
	}
	if adapter.active.Load() != 0 {
		t.Fatalf("active adapter workers = %d, want 0", adapter.active.Load())
	}
	if runtime.closeCalls.Load() != 1 {
		t.Fatalf("runtime closes = %d, want 1", runtime.closeCalls.Load())
	}
	for _, workerID := range []string{"w1", "w2"} {
		snapshot, snapshotErr := runtime.Snapshot(workerID)
		if snapshotErr != nil {
			t.Fatalf("snapshot %s: %v", workerID, snapshotErr)
		}
		if snapshot.State != syncpoint.WorkerStateDone {
			t.Fatalf("worker %s state = %s, want done", workerID, snapshot.State)
		}
	}

	t.Log(
		"ORCHESTRATOR_RUN_RESULT schedule=sch_ba00582f9632 workers=2 steps=4 " +
			"timeouts=1 pending_resolved=1 terminal_done=2 cleanup=ok",
	)
}

func TestRunDefersPointBehindPendingWorkerArrival(t *testing.T) {
	fixtureRunner := &recordingFixture{}
	runtime := newRuntimeProbe()
	adapter := newScriptedAdapter(runtime)
	orchestrator := newTestOrchestrator(t, Config{
		Fixture:               fixtureRunner,
		DB:                    &fixture.DB{},
		NewRuntime:            func() syncpoint.Runtime { return runtime },
		NewAdapter:            func(syncpoint.Client) sut.Adapter { return adapter },
		BlockInferenceTimeout: testBlockTimeout,
		StepTimeout:           testStepTimeout,
		RunTimeout:            testRunTimeout,
		StopTimeout:           testStopTimeout,
	})

	schedule, err := scenario.NewSchedule([]scenario.CoordinationStep{
		{Worker: "w1", Point: "after_read_request"},
		{Worker: "w2", Point: "after_read_request"},
		{Worker: "w2", Point: "before_insert_assignment"},
		{Worker: "w1", Point: "before_insert_assignment"},
	})
	if err != nil {
		t.Fatalf("create dependent-point schedule: %v", err)
	}

	result, err := orchestrator.Run(
		context.Background(),
		matchingScenario(),
		schedule,
		stableObserver,
	)
	if err != nil {
		t.Fatalf("run dependent-point schedule: %v", err)
	}
	if result.Timeouts != 1 || result.PendingResolved != 1 {
		t.Fatalf(
			"dependent-point progress timeouts=%d pending_resolved=%d, want 1/1",
			result.Timeouts,
			result.PendingResolved,
		)
	}

	var released []int
	var skipped []int
	for _, event := range result.Trace {
		switch event.Kind {
		case EventPointReleased:
			released = append(released, event.Step)
		case EventStepTerminalSkipped:
			skipped = append(skipped, event.Step)
		}
	}
	if !reflect.DeepEqual(released, []int{0, 3, 1}) {
		t.Fatalf("dependent-point releases = %v, want [0 3 1]", released)
	}
	if !reflect.DeepEqual(skipped, []int{2}) {
		t.Fatalf("dependent-point skips = %v, want [2]", skipped)
	}
}

func TestDrainPendingClearsStepWhenPollCollectsTerminal(t *testing.T) {
	runtime := &timeoutWaitRuntime{Runtime: syncpoint.New()}
	t.Cleanup(runtime.Close)
	collected := make(chan collectedResult, 1)
	collected <- collectedResult{result: sut.WorkerResult{WorkerID: "w1"}}
	close(collected)

	result := RunResult{}
	execution := &workerExecution{
		worker:          scenario.Worker{ID: "w1", Command: "assign"},
		collectedResult: collected,
	}
	coordinator := runCoordinator{
		ctx:     context.Background(),
		runtime: runtime,
		value:   scenario.Scenario{SyncPoints: []string{"after_read_request"}},
		schedule: scenario.Schedule{Steps: []scenario.CoordinationStep{
			{Worker: "w1", Point: "after_read_request"},
		}},
		result:     &result,
		trace:      newTraceRecorder(nil),
		executions: map[string]*workerExecution{"w1": execution},
		pending:    map[int]bool{0: true},
	}

	progressed, err := coordinator.drainPending()
	if err != nil {
		t.Fatalf("drain terminal pending step: %v", err)
	}
	if !progressed {
		t.Fatal("drain terminal pending step progressed = false, want true")
	}
	if len(coordinator.pending) != 0 {
		t.Fatalf("pending steps = %v, want none", coordinator.pending)
	}
	if !execution.terminal || execution.terminalState != TerminalStateDone {
		t.Fatalf("execution terminal = %+v, want done", execution)
	}
	if result.Timeouts != 0 {
		t.Fatalf("timeouts = %d, want 0 after terminal poll", result.Timeouts)
	}

	wantKinds := []EventKind{EventWorkerDone, EventStepTerminalSkipped}
	var gotKinds []EventKind
	for _, event := range coordinator.trace.events {
		gotKinds = append(gotKinds, event.Kind)
	}
	if !reflect.DeepEqual(gotKinds, wantKinds) {
		t.Fatalf("terminal pending trace = %v, want %v", gotKinds, wantKinds)
	}
}

func TestRunCleanup(t *testing.T) {
	rootErr := errors.New("injected run failure")
	cleanupErr := errors.New("injected cleanup failure")

	t.Run("reset", func(t *testing.T) {
		fixtureRunner := &recordingFixture{resetErr: rootErr}
		factoryCalls := 0
		orchestrator := newTestOrchestrator(t, Config{
			Fixture: fixtureRunner,
			DB:      &fixture.DB{},
			NewRuntime: func() syncpoint.Runtime {
				factoryCalls++
				return syncpoint.New()
			},
			NewAdapter:            func(syncpoint.Client) sut.Adapter { return newScriptedAdapter(nil) },
			BlockInferenceTimeout: testBlockTimeout,
			StepTimeout:           testStepTimeout,
			RunTimeout:            testRunTimeout,
			StopTimeout:           testStopTimeout,
		})

		_, err := orchestrator.Run(
			context.Background(),
			matchingScenario(),
			matchingSchedule(t),
			stableObserver,
		)
		if !errors.Is(err, rootErr) {
			t.Fatalf("reset error = %v, want errors.Is(_, rootErr)", err)
		}
		if factoryCalls != 0 {
			t.Fatalf("runtime factory calls after reset failure = %d, want 0", factoryCalls)
		}
	})

	tests := []struct {
		name      string
		configure func(*runtimeProbe, *scriptedAdapter)
		observer  Observer
	}{
		{
			name: "start",
			configure: func(_ *runtimeProbe, adapter *scriptedAdapter) {
				adapter.startErr = rootErr
			},
		},
		{
			name: "register",
			configure: func(runtime *runtimeProbe, _ *scriptedAdapter) {
				runtime.failRegister = rootErr
			},
		},
		{
			name: "invoke",
			configure: func(_ *runtimeProbe, adapter *scriptedAdapter) {
				adapter.invokeErr = rootErr
			},
		},
		{
			name: "wait",
			configure: func(runtime *runtimeProbe, _ *scriptedAdapter) {
				runtime.failWait = rootErr
			},
		},
		{
			name: "release",
			configure: func(runtime *runtimeProbe, _ *scriptedAdapter) {
				runtime.failRelease = rootErr
			},
		},
		{
			name: "collector",
			configure: func(runtime *runtimeProbe, _ *scriptedAdapter) {
				runtime.failFinish = rootErr
			},
		},
		{
			name:      "observer",
			configure: func(_ *runtimeProbe, _ *scriptedAdapter) {},
			observer: func(context.Context, *fixture.DB, RunResult) (string, error) {
				return "", rootErr
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixtureRunner := &recordingFixture{}
			runtime := newRuntimeProbe()
			adapter := newScriptedAdapter(runtime)
			adapter.stopErr = cleanupErr
			test.configure(runtime, adapter)
			observer := test.observer
			if observer == nil {
				observer = stableObserver
			}

			orchestrator := newTestOrchestrator(t, Config{
				Fixture:               fixtureRunner,
				DB:                    &fixture.DB{},
				NewRuntime:            func() syncpoint.Runtime { return runtime },
				NewAdapter:            func(syncpoint.Client) sut.Adapter { return adapter },
				BlockInferenceTimeout: testBlockTimeout,
				StepTimeout:           testStepTimeout,
				RunTimeout:            testRunTimeout,
				StopTimeout:           testStopTimeout,
			})

			_, err := orchestrator.Run(
				context.Background(),
				matchingScenario(),
				matchingSchedule(t),
				observer,
			)
			if !errors.Is(err, rootErr) {
				t.Fatalf("run error = %v, want errors.Is(_, rootErr)", err)
			}
			if !errors.Is(err, cleanupErr) {
				t.Fatalf("run error = %v, want errors.Is(_, cleanupErr)", err)
			}
			if adapter.stopCalls.Load() != 1 {
				t.Fatalf("adapter stops = %d, want 1", adapter.stopCalls.Load())
			}
			if adapter.active.Load() != 0 {
				t.Fatalf("active adapter workers = %d, want 0", adapter.active.Load())
			}
			if runtime.closeCalls.Load() != 1 {
				t.Fatalf("runtime closes = %d, want 1", runtime.closeCalls.Load())
			}
			if fixtureRunner.resetCalls != 1 {
				t.Fatalf("fixture resets = %d, want 1", fixtureRunner.resetCalls)
			}
		})
	}
}

func TestRunRejectsInvalidConfig(t *testing.T) {
	base := Config{
		Fixture:               &recordingFixture{},
		DB:                    &fixture.DB{},
		NewRuntime:            syncpoint.New,
		NewAdapter:            func(client syncpoint.Client) sut.Adapter { return newScriptedAdapter(client) },
		BlockInferenceTimeout: testBlockTimeout,
		StepTimeout:           testStepTimeout,
		RunTimeout:            testRunTimeout,
		StopTimeout:           testStopTimeout,
	}
	tests := []struct {
		name     string
		mutate   func(*Config)
		wantPart string
	}{
		{name: "fixture", mutate: func(config *Config) { config.Fixture = nil }, wantPart: "fixture"},
		{name: "database", mutate: func(config *Config) { config.DB = nil }, wantPart: "database"},
		{name: "runtime factory", mutate: func(config *Config) { config.NewRuntime = nil }, wantPart: "runtime factory"},
		{name: "adapter factory", mutate: func(config *Config) { config.NewAdapter = nil }, wantPart: "adapter factory"},
		{
			name:     "block timeout",
			mutate:   func(config *Config) { config.BlockInferenceTimeout = 0 },
			wantPart: "block inference timeout",
		},
		{name: "step timeout", mutate: func(config *Config) { config.StepTimeout = 0 }, wantPart: "step timeout"},
		{name: "run timeout", mutate: func(config *Config) { config.RunTimeout = 0 }, wantPart: "run timeout"},
		{name: "stop timeout", mutate: func(config *Config) { config.StopTimeout = 0 }, wantPart: "stop timeout"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := base
			test.mutate(&config)
			_, err := New(config)
			if err == nil || !strings.Contains(err.Error(), test.wantPart) {
				t.Fatalf("New error = %v, want containing %q", err, test.wantPart)
			}
		})
	}

	orchestrator := newTestOrchestrator(t, base)
	_, err := orchestrator.Run(
		context.Background(),
		matchingScenario(),
		matchingSchedule(t),
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "observer is required") {
		t.Fatalf("nil observer error = %v, want observer context", err)
	}
}

func newTestOrchestrator(t *testing.T, config Config) *Orchestrator {
	t.Helper()

	orchestrator, err := New(config)
	if err != nil {
		t.Fatalf("new orchestrator: %v", err)
	}
	return orchestrator
}

func stableObserver(context.Context, *fixture.DB, RunResult) (string, error) {
	return "stable", nil
}

func matchingScenario() scenario.Scenario {
	return scenario.Scenario{
		Name: "matching-concurrent-assign",
		Workers: []scenario.Worker{
			{ID: "w1", Command: "assign"},
			{ID: "w2", Command: "assign"},
		},
		SyncPoints: []string{"after_read_request", "before_insert_assignment"},
		SUTConfig: sut.SUTConfig{
			Variant: "fixed",
			Params:  map[string]string{"request_id": "42"},
		},
	}
}

func matchingSchedule(t *testing.T) scenario.Schedule {
	t.Helper()

	schedule, err := scenario.NewSchedule([]scenario.CoordinationStep{
		{Worker: "w1", Point: "after_read_request"},
		{Worker: "w2", Point: "after_read_request"},
		{Worker: "w1", Point: "before_insert_assignment"},
		{Worker: "w2", Point: "before_insert_assignment"},
	})
	if err != nil {
		t.Fatalf("new matching schedule: %v", err)
	}
	return schedule
}

type recordingFixture struct {
	resetCalls int
	resetErr   error
}

func (*recordingFixture) Provision(context.Context, fixture.FixtureSpec) (*fixture.DB, error) {
	return nil, errors.New("recording fixture does not provision")
}

func (f *recordingFixture) Reset(context.Context) error {
	f.resetCalls++
	return f.resetErr
}

func (*recordingFixture) Teardown(context.Context) error {
	return nil
}

type runtimeProbe struct {
	syncpoint.Runtime

	failRegister error
	failWait     error
	failRelease  error
	failFinish   error

	closeCalls atomic.Int32
}

type timeoutWaitRuntime struct {
	syncpoint.Runtime
}

func (r *timeoutWaitRuntime) WaitArrive(
	context.Context,
	string,
	string,
	time.Duration,
) (syncpoint.ArriveStatus, error) {
	return syncpoint.ArriveStatusTimeout, nil
}

func newRuntimeProbe() *runtimeProbe {
	return &runtimeProbe{Runtime: syncpoint.New()}
}

func (r *runtimeProbe) Register(workerID string) error {
	if r.failRegister != nil {
		return r.failRegister
	}
	return r.Runtime.Register(workerID)
}

func (r *runtimeProbe) WaitArrive(
	ctx context.Context,
	workerID string,
	point string,
	timeout time.Duration,
) (syncpoint.ArriveStatus, error) {
	if r.failWait != nil {
		return syncpoint.ArriveStatusUnknown, r.failWait
	}
	return r.Runtime.WaitArrive(ctx, workerID, point, timeout)
}

func (r *runtimeProbe) Release(ctx context.Context, workerID, point string) error {
	if r.failRelease != nil {
		return r.failRelease
	}
	return r.Runtime.Release(ctx, workerID, point)
}

func (r *runtimeProbe) Finish(workerID string, workerErr error) error {
	if r.failFinish != nil {
		return r.failFinish
	}
	return r.Runtime.Finish(workerID, workerErr)
}

func (r *runtimeProbe) Close() {
	r.closeCalls.Add(1)
	r.Runtime.Close()
}

type scriptedAdapter struct {
	client syncpoint.Client

	startErr        error
	invokeErr       error
	stopErr         error
	keepResultsOpen bool

	mu       sync.Mutex
	ctx      context.Context
	cancel   context.CancelFunc
	wait     sync.WaitGroup
	w1Result chan struct{}
	w1Once   sync.Once

	active    atomic.Int32
	stopCalls atomic.Int32
	stopped   atomic.Bool
}

func newScriptedAdapter(client syncpoint.Client) *scriptedAdapter {
	return &scriptedAdapter{
		client:   client,
		w1Result: make(chan struct{}),
	}
}

func (a *scriptedAdapter) Start(
	ctx context.Context,
	_ sut.SUTConfig,
	_ *fixture.DB,
) (sut.Handle, error) {
	if a.startErr != nil {
		return nil, a.startErr
	}

	a.mu.Lock()
	a.ctx, a.cancel = context.WithCancel(ctx)
	a.mu.Unlock()
	return a, nil
}

func (a *scriptedAdapter) Invoke(
	_ context.Context,
	workerID string,
	_ string,
) (<-chan sut.WorkerResult, error) {
	if a.invokeErr != nil {
		return nil, a.invokeErr
	}

	a.mu.Lock()
	ctx := a.ctx
	a.mu.Unlock()
	if ctx == nil {
		return nil, errors.New("scripted adapter was not started")
	}

	results := make(chan sut.WorkerResult, 1)
	a.wait.Add(1)
	a.active.Add(1)
	go func() {
		defer a.wait.Done()
		defer a.active.Add(-1)
		if !a.keepResultsOpen {
			defer close(results)
		}

		var workerErr error
		switch workerID {
		case "w1":
			workerErr = a.client.Arrive(ctx, workerID, "after_read_request")
			if workerErr == nil {
				workerErr = a.client.Arrive(ctx, workerID, "before_insert_assignment")
			}
			results <- sut.WorkerResult{WorkerID: workerID, Err: workerErr}
			a.w1Once.Do(func() { close(a.w1Result) })
		case "w2":
			select {
			case <-a.w1Result:
				workerErr = a.client.Arrive(ctx, workerID, "after_read_request")
			case <-ctx.Done():
				workerErr = ctx.Err()
			}
			results <- sut.WorkerResult{WorkerID: workerID, Err: workerErr}
		default:
			results <- sut.WorkerResult{
				WorkerID: workerID,
				Err:      fmt.Errorf("unsupported scripted worker %q", workerID),
			}
		}
	}()
	return results, nil
}

func (a *scriptedAdapter) Stop(ctx context.Context) error {
	a.stopCalls.Add(1)
	a.stopped.Store(true)
	a.mu.Lock()
	cancel := a.cancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}

	done := make(chan struct{})
	go func() {
		a.wait.Wait()
		close(done)
	}()
	select {
	case <-done:
		return a.stopErr
	case <-ctx.Done():
		return errors.Join(a.stopErr, fmt.Errorf("stop scripted adapter: %w", ctx.Err()))
	}
}
