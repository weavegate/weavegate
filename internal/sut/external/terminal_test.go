package external

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
)

func TestValidatedTerminalSurvivesSessionFault(t *testing.T) {
	for _, failure := range []string{"fatal", "process_death", "cleanup_cutoff"} {
		for _, transaction := range []string{"committed", "rolled_back", "unknown"} {
			t.Run(failure+"/"+transaction, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					p := newPeer(t)
					held, resume := make(chan struct{}), make(chan struct{})
					p.a.beforeRelease = func() { close(held); <-resume }
					p.start()
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					w, stream := p.invoke(ctx, "w1")
					p.send("arrive", arrivalBody(w, 1, "after_read"))
					call := <-p.client.calls
					call.result <- nil
					<-held
					cancel()
					p.read("cancel")
					if transaction != "unknown" {
						var wireErr any
						if transaction == "rolled_back" {
							wireErr = wireError("cancelled", "cancelled by context", 0, "")
						}
						p.send("terminal", terminalBody(w, transaction, wireErr))
					}
					synctest.Wait()
					p.a.mu.Lock()
					inv := p.a.invocations[str(w.Body["invocation"])]
					validated := inv.terminal != nil
					bridges := inv.bridges
					duration := inv.terminalAt.Sub(inv.started)
					p.a.mu.Unlock()
					if validated != (transaction != "unknown") || bridges != 1 {
						t.Fatal("terminal acceptance/held bridge precondition not reached")
					}
					switch failure {
					case "fatal":
						p.send("fatal", map[string]any{"kind": "transaction", "message": "synthetic failure"})
						<-p.a.Faults().Done()
						if transaction == "unknown" {
							// A terminal arriving after fault must not create evidence.
							p.send("terminal", terminalBody(w, "committed", nil))
						}
						p.exit(errors.New("fatal exit"))
					case "process_death":
						p.exit(errors.New("process died"))
					case "cleanup_cutoff":
						go func() { _ = p.a.Stop(context.Background()) }()
						p.read("stop")
						<-p.killed
					}
					<-p.a.Faults().Done()
					fault := p.a.Faults().Err()
					synctest.Wait()
					select {
					case <-stream:
						t.Fatal("fault published or closed the stream before bridge unwinding")
					default:
					}
					close(resume)
					if transaction == "unknown" {
						if _, ok := <-stream; ok {
							t.Fatal("session fault fabricated an outcome")
						}
					} else {
						result := outcome(t, stream)
						if result.Worker == nil || result.Unstarted != nil || result.Worker.WorkerID != "w1" {
							t.Fatal("validated worker outcome lost or changed")
						}
						if transaction == "committed" && result.Worker.Err != nil {
							t.Fatal("committed result rewritten as an error")
						}
						if transaction == "rolled_back" && !errors.Is(result.Worker.Err, context.Canceled) {
							t.Fatal("validated rollback error lost")
						}
						if result.Worker.Duration != duration {
							t.Fatal("terminal receipt duration changed during cleanup")
						}
					}
					if err := p.a.Stop(context.Background()); err == nil || !errors.Is(err, fault) {
						t.Fatal("publishing the known outcome cleared the session failure", err)
					}
				})
			})
		}
	}
	t.Log("EXTERNAL_SUT_TERMINAL_FAULT_RESULT validated=preserved unknown=absent late_terminal=ignored publication=after_bridge stop=error")
}
