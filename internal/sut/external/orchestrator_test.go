package external

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/weavegate/weavegate/internal/fixture"
	"github.com/weavegate/weavegate/internal/oracle"
	"github.com/weavegate/weavegate/internal/orchestrator"
	"github.com/weavegate/weavegate/internal/scenario"
	"github.com/weavegate/weavegate/internal/sut"
	"github.com/weavegate/weavegate/internal/syncpoint"
)

// Only provisioning is stubbed: the real orchestrator owns runtime Finish,
// evaluation invalidation, cancellation, result collection and final Stop.
type preparedAdapter struct{ *adapter }

func (a preparedAdapter) Start(ctx context.Context, _ sut.SUTConfig, _ *fixture.DB) (sut.Handle, error) {
	return a.start(ctx, startBody())
}

type idleFixture struct{}

func (idleFixture) Reset(context.Context) error    { return nil }
func (idleFixture) Teardown(context.Context) error { return nil }

func TestOrchestratorLateFaultAndFingerprint(t *testing.T) {
	var healthyFingerprint string
	for _, mode := range []string{"healthy", "healthy_repeat", "wire_fatal", "process_death", "context_cancel"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := newPeer(t)
				p.a.id = randomID
				evaluating, returnEvaluation := make(chan context.Context, 1), make(chan struct{})
				eval := oracle.EvaluatorFunc(func(ctx context.Context, _ oracle.DB, _ oracle.RunContext) (oracle.Evaluation, error) {
					evaluating <- ctx
					<-returnEvaluation
					return oracle.NewEvaluation(oracle.OracleResult{OracleID: "synthetic-pass"})
				})
				o, err := orchestrator.New(orchestrator.Config{Fixture: idleFixture{}, DB: &fixture.DB{}, NewRuntime: syncpoint.New,
					NewAdapter:            func(client syncpoint.Client) sut.Adapter { p.a.client = client; return preparedAdapter{p.a} },
					BlockInferenceTimeout: time.Second, StepTimeout: time.Second, RunTimeout: 20 * time.Second, StopTimeout: 5 * time.Second})
				if err != nil {
					t.Fatal(err)
				}
				schedule, err := scenario.NewSchedule([]scenario.CoordinationStep{{Worker: "w1", Point: "after_read"}})
				if err != nil {
					t.Fatal(err)
				}
				value := scenario.Scenario{Name: "external-lifecycle", Workers: []scenario.Worker{{ID: "w1", Command: "assign"}}, SyncPoints: []string{"after_read"}}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				type runResult struct {
					result orchestrator.RunResult
					err    error
				}
				done := make(chan runResult, 1)
				go func() { r, e := o.Run(ctx, value, schedule, eval); done <- runResult{r, e} }()
				p.read("start")
				p.send("ready", map[string]any{"commands": []string{"assign"}, "points": []string{"after_read", "before_write"}, "capacity": 2})
				w := p.read("invoke")
				p.send("accepted", binding(w))
				p.send("arrive", arrivalBody(w, 1, "after_read"))
				p.read("release")
				p.send("terminal", terminalBody(w, "committed", nil))
				evalCtx := <-evaluating
				switch mode {
				case "wire_fatal":
					p.send("fatal", map[string]any{"kind": "transaction", "message": "private SQL and credentials"})
					<-p.a.Faults().Done()
					<-evalCtx.Done()
					p.exit(errors.New("fatal exit"))
				case "process_death":
					p.exit(errors.New("child died"))
					<-p.a.Faults().Done()
					<-evalCtx.Done()
				case "context_cancel":
					cancel()
					<-evalCtx.Done()
				}
				close(returnEvaluation)
				if mode == "healthy" || mode == "healthy_repeat" || mode == "context_cancel" {
					p.read("stop")
					p.send("stopped", map[string]any{})
					p.exit(nil)
				}
				r := <-done
				if len(r.result.Workers) != 1 || r.result.Workers[0].Err != nil {
					t.Fatal("committed worker fact lost", r.err)
				}
				if mode == "healthy" || mode == "healthy_repeat" {
					if r.err != nil || r.result.Fingerprint == "" {
						t.Fatal("normal run failed", r.err)
					}
					if healthyFingerprint == "" {
						healthyFingerprint = r.result.Fingerprint
					} else if r.result.Fingerprint != healthyFingerprint {
						t.Fatal("volatile wire identity changed fingerprint")
					}
				} else {
					if r.err == nil || r.result.Fingerprint != "" || len(r.result.Evaluation.Results) != 0 {
						t.Fatal("late failure left provisional success", r.err)
					}
					if mode == "context_cancel" && !errors.Is(r.err, context.Canceled) {
						t.Fatal("operation cancellation lost")
					}
					if mode == "process_death" && !errors.Is(r.err, errTransport) {
						t.Fatal("transport cause lost")
					}
				}
				select {
				case <-p.a.exitDone:
				default:
					t.Fatal("Run returned before reaping")
				}
			})
		})
	}
	t.Log("EXTERNAL_SUT_RUN_RESULT late_fatal=invalidates late_death=invalidates committed_result=preserved cancellation=run_error fingerprints=stable")
}
