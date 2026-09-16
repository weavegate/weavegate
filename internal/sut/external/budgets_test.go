package external

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestElapsedStopBudget(t *testing.T) {
	for _, c := range []struct {
		name    string
		elapsed time.Duration
		want    int
	}{
		{"zero", 0, 2500}, {"delayed", 1250 * time.Millisecond, 1250}, {"submillisecond", 2499500 * time.Microsecond, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := newPeer(t)
				p.a.beforeWrite = func(kind string) {
					if kind == "stop" {
						p.a.mu.Lock()
						now := p.a.graceDeadline.Add(-2500 * time.Millisecond).Add(c.elapsed)
						p.a.now = func() time.Time { return now }
						p.a.mu.Unlock()
					}
				}
				p.start()
				done := make(chan error, 1)
				go func() { done <- p.a.Stop(context.Background()) }()
				if c.want == 0 {
					if f, _, err := readFrame(p.input); err == nil {
						t.Fatalf("submillisecond budget emitted %s", f.Type)
					}
					if err := <-done; err == nil {
						t.Fatal("exhausted budget succeeded")
					}
				} else {
					f := p.read("stop")
					if got := number(f.Body["budget_ms"]); got != c.want {
						t.Fatalf("budget %d, want %d", got, c.want)
					}
					p.send("stopped", map[string]any{})
					p.exit(nil)
					if err := <-done; err != nil {
						t.Fatal(err)
					}
				}
			})
		})
	}
	reportCheck(t, "requirement/elapsed-stop-budget", "observe/evidence", "internal/sut/external/budgets_test.go:TestElapsedStopBudget")
}

func TestEarlierStopCallerCannotResetSharedDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPeer(t)
		p.start()
		first := make(chan error, 1)
		go func() { first <- p.a.Stop(context.Background()) }()
		p.read("stop")
		p.a.mu.Lock()
		deadline := p.a.stopDeadline
		p.a.mu.Unlock()
		short, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		defer cancel()
		if err := p.a.Stop(short); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("short caller did not retain its deadline", err)
		}
		p.a.mu.Lock()
		unchanged := deadline.Equal(p.a.stopDeadline)
		p.a.mu.Unlock()
		if !unchanged {
			t.Fatal("second caller reset shared budget")
		}
		p.send("stopped", map[string]any{})
		p.exit(nil)
		if err := <-first; err != nil {
			t.Fatal("short caller poisoned normal shared cleanup", err)
		}
	})
}

func TestTerminalWaitsForCanceledBridge(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPeer(t)
		held, resume := make(chan struct{}), make(chan struct{})
		p.a.beforeRelease = func() { close(held); <-resume }
		p.start()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		w, ch := p.invoke(ctx, "w1")
		p.send("arrive", arrivalBody(w, 1, "after_read"))
		call := <-p.client.calls
		call.result <- nil
		<-held
		cancel()
		p.read("cancel")
		p.send("terminal", terminalBody(w, "rolled_back", wireError("cancelled", "cancelled by context", 0, "")))
		synctest.Wait()
		select {
		case <-ch:
			t.Fatal("result published before bridge unwound")
		default:
		}
		if _, err := p.a.Invoke(context.Background(), "w1", "assign"); err == nil {
			t.Fatal("worker reused while bridge active")
		}
		close(resume)
		outcome(t, ch)
		p.stop()
	})
}

func TestReadinessWaitsForStartWrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPeer(t)
		result := p.startAsync(context.Background())
		synctest.Wait() // The start writer is blocked on the unread owned pipe.
		p.send("ready", map[string]any{"commands": []string{"assign"}, "points": []string{"after_read", "before_write"}, "capacity": 2})
		synctest.Wait()
		select {
		case <-result:
			t.Fatal("Start completed before start frame delivery")
		default:
		}
		p.read("start")
		if err := <-result; err != nil {
			t.Fatal(err)
		}
		p.stop()
	})
}

func TestAcceptedBeforeInvokeWriteIsRejected(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPeer(t)
		held, resume := make(chan struct{}), make(chan struct{})
		p.a.beforeWrite = func(kind string) {
			if kind == "invoke" {
				close(held)
				<-resume
			}
		}
		p.start()
		ch, err := p.a.Invoke(context.Background(), "w1", "assign")
		if err != nil {
			t.Fatal(err)
		}
		<-held
		p.a.mu.Lock()
		id := p.a.workers["w1"].id
		p.a.mu.Unlock()
		p.send("accepted", map[string]any{"invocation": id, "worker": "w1"})
		<-p.a.Faults().Done()
		if _, ok := <-ch; ok {
			t.Fatal("accepted before dispatch manufactured a result")
		}
		close(resume)
		p.read("invoke")
		p.read("fatal")
		p.exit(errProtocol)
		if err := p.a.Stop(context.Background()); !errors.Is(err, errProtocol) {
			t.Fatal(err)
		}
	})
	reportCheck(t, "requirement/invoke-dispatch-receipt", "observe/evidence", "internal/sut/external/budgets_test.go:TestAcceptedBeforeInvokeWriteIsRejected")
}

func TestStopBeforeQueuedStartDropsCredentials(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPeer(t)
		held, resume := make(chan struct{}), make(chan struct{})
		p.a.beforeWrite = func(kind string) {
			if kind == "start" {
				close(held)
				<-resume
			}
		}
		body := startBody()
		started := make(chan error, 1)
		go func() { _, err := p.a.start(context.Background(), body); started <- err }()
		<-held
		stopped := make(chan error, 1)
		go func() { stopped <- p.a.Stop(context.Background()) }()
		synctest.Wait()
		close(resume)
		if err := <-stopped; err == nil {
			t.Fatal("uninitialized cleanup reported normal Stop")
		}
		if err := <-started; err == nil {
			t.Fatal("Start returned a handle")
		}
		if len(body) != 0 {
			t.Fatal("abandoned start retained credentials")
		}
		p.a.mu.Lock()
		sent := p.a.startSent
		p.a.mu.Unlock()
		if sent {
			t.Fatal("Stop allowed queued startup to initialize the peer")
		}
	})
}
