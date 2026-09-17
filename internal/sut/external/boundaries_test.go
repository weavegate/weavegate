package external

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestStartRejectsInvalidUTF8BeforeLaunch(t *testing.T) {
	for _, field := range []string{"variant", "param_key", "param_value", "database"} {
		t.Run(field, func(t *testing.T) {
			p := newPeer(t)
			body := startBody()
			bad := "invalid\xff"
			switch field {
			case "variant":
				body["variant"] = bad
			case "param_key":
				body["params"] = map[string]string{bad: "value"}
			case "param_value":
				body["params"] = map[string]string{"key": bad}
			case "database":
				body["database"].(map[string]any)["password"] = bad
			}
			launched := false
			p.a.launch = func(string, string) (*child, error) {
				launched = true
				return nil, errTransport
			}
			_, err := p.a.start(context.Background(), body)
			if launched || !errors.Is(err, errProtocol) {
				t.Fatalf("invalid UTF-8 reached launch: launched=%v err=%v", launched, err)
			}
		})
	}
}

func TestStartupExpiryLatchesFatal(t *testing.T) {
	for _, mode := range []string{"startup_budget", "context_deadline", "context_cancel"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := newPeer(t)
				ctx, cancel := context.WithCancel(context.Background())
				if mode == "context_deadline" {
					cancel()
					ctx, cancel = context.WithTimeout(context.Background(), time.Second)
				}
				defer cancel()
				started := p.startAsync(ctx)
				p.read("start")
				if mode == "context_cancel" {
					cancel()
					p.read("stop")
					p.send("stopped", map[string]any{})
					p.exit(nil)
					if err := <-started; !errors.Is(err, context.Canceled) {
						t.Fatal(err)
					}
					if err := p.a.Stop(context.Background()); err != nil {
						t.Fatal("cancellation forbade normal cleanup", err)
					}
					return
				}
				f := p.read("fatal")
				if f.Body["kind"] != "startup" {
					t.Fatal("wrong fatal kind", f.Body)
				}
				// A misleading normal acknowledgement cannot erase expiry.
				p.read("stop")
				p.send("stopped", map[string]any{})
				p.exit(nil)
				if err := <-started; !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("startup deadline was not preserved", err)
				}
				if err := p.a.Stop(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("expiry allowed normal cleanup", err)
				}
			})
		})
	}
}

func TestStoppedWaitsForStopDelivery(t *testing.T) {
	for _, broken := range []bool{false, true} {
		t.Run(map[bool]string{false: "delivered", true: "broken"}[broken], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := newPeer(t)
				p.start()
				done := make(chan error, 1)
				go func() { done <- p.a.Stop(context.Background()) }()
				synctest.Wait() // Stop is blocked on the unread pipe.
				p.send("stopped", map[string]any{})
				synctest.Wait()
				p.a.mu.Lock()
				acknowledged := p.a.stopped
				p.a.mu.Unlock()
				if acknowledged {
					t.Error("stopped accepted before stop delivery")
				}
				if !broken {
					p.read("stop")
				}
				p.exit(nil)
				if err := <-done; (err != nil) != broken {
					t.Fatalf("broken=%v Stop=%v", broken, err)
				}
			})
		})
	}
}
