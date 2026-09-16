package external

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/weavegate/weavegate/internal/orchestrator"
	"github.com/weavegate/weavegate/internal/sut"
)

// This runner deliberately records only checks with implemented observers.
// The manifest builder retains every other applicable case as incomplete; no
// default branch can turn an unknown assertion or event into passing evidence.
func TestSharedLifecycleSubset(t *testing.T) {
	v := loadVectors(t)
	selected := []string{"incremented_arrival", "rollback", "mysql_deadlock", "mysql_lock_timeout", "post_commit_exception", "cancel_at_arrival", "late_arrival_after_cancel", "stale_session", "duplicate_frame", "retired_invocation_worker_reuse", "retired_terminal_identical"}
	for _, c := range v.Cases {
		if !slices.Contains(selected, c.ID) {
			continue
		}
		t.Run(c.ID, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := newPeer(t)
				p.a.id = func() (string, error) { return strings.Repeat("2", 32), nil }
				h := &vectorHarness{p: p, outputs: make(chan frame, 128), streams: map[string]<-chan sut.InvocationOutcome{}, outcomes: map[string]sut.InvocationOutcome{}, contexts: map[string]context.Context{}, cancels: map[string]context.CancelFunc{}, calls: map[string]*arrivalCall{}}
				go func() {
					for {
						f, _, err := readFrame(p.input)
						if err != nil {
							return
						}
						h.outputs <- f
					}
				}()
				h.start = p.startAsync(context.Background())
				steps := append(expandPrefix(t, v, c.Prefix), c.Steps...)
				for i, s := range steps {
					h.step(t, "case/"+c.ID, i, s)
				}
			})
		})
	}
	t.Log("EXTERNAL_SUT_VECTOR_SUBSET_RESULT cases=11 dispatch=closed assertions=observed acceptance=incomplete")
}

func expandPrefix(t *testing.T, v vectors, name string) []vectorStep {
	t.Helper()
	steps, ok := v.Prefixes[name]
	if !ok {
		t.Fatal("unknown vector prefix")
	}
	var result []vectorStep
	for _, s := range steps {
		if s.Prefix != "" {
			result = append(result, expandPrefix(t, v, s.Prefix)...)
		} else {
			result = append(result, s)
		}
	}
	return result
}

type vectorHarness struct {
	p         *peer
	start     <-chan error
	outputs   chan frame
	pending   []frame
	streams   map[string]<-chan sut.InvocationOutcome
	outcomes  map[string]sut.InvocationOutcome
	contexts  map[string]context.Context
	cancels   map[string]context.CancelFunc
	calls     map[string]*arrivalCall
	callCount int
}

func reportCheck(t *testing.T, row, check, handler string) {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"row": row, "check": check, "handler": handler})
	t.Logf("EXTERNAL_SUT_CHECK %s", raw)
}

func (h *vectorHarness) step(t *testing.T, row string, index int, s vectorStep) {
	t.Helper()
	base := fmt.Sprintf("step/%d", index)
	if s.Peer == "java" {
		if s.Delivery != "exchange" {
			return
		}
		want, err := decodeFrame(s.Frame)
		if err != nil {
			t.Fatal(err)
		}
		var got frame
		if len(h.pending) > 0 {
			got = h.pending[0]
			h.pending = h.pending[1:]
		} else {
			got = <-h.outputs
		}
		assertFrame(t, got, want)
		reportCheck(t, row, base+"/output/"+want.Type, "internal/sut/external/vectors_test.go:vectorHarness.step")
		return
	}
	h.p.a.mu.Lock()
	beforeSeq := h.p.a.received
	h.p.a.mu.Unlock()
	beforeCalls := h.callCount
	var f frame
	var id string
	if s.Action == "receive" {
		var err error
		f, err = decodeFrame(s.Frame)
		if err != nil {
			t.Fatal(err)
		}
		id = str(f.Body["invocation"])
		h.p.sendFrame(f)
		reportCheck(t, row, base+"/receive/"+f.Type, "internal/sut/external/vectors_test.go:vectorHarness.step")
	} else if s.Action == "local" {
		args := map[string]any{}
		if err := json.Unmarshal(s.Args, &args); err != nil {
			t.Fatal(err)
		}
		switch s.Event {
		case "invoke_call":
			if !fields(args, "invocation worker command context") || args["context"] != "fresh" || !identity(args["invocation"]) || !name(args["worker"]) || !name(args["command"]) {
				t.Fatal("invalid invoke_call arguments")
			}
			id = str(args["invocation"])
			h.p.a.mu.Lock()
			h.p.a.id = func() (string, error) { return id, nil }
			h.p.a.mu.Unlock()
			ctx, cancel := context.WithCancel(context.Background())
			h.contexts[id], h.cancels[id] = ctx, cancel
			ch, err := h.p.a.Invoke(ctx, str(args["worker"]), str(args["command"]))
			if err != nil {
				t.Fatal(err)
			}
			h.streams[id] = ch
		case "runtime_arrive_returns":
			if !fields(args, "identity result") {
				t.Fatal("invalid runtime return arguments")
			}
			ident, ok := args["identity"].(map[string]any)
			if !ok || !fields(ident, "invocation worker arrival point") {
				t.Fatal("invalid arrival identity")
			}
			id = str(ident["invocation"])
			call := h.calls[id+"/"+str(ident["arrival"])]
			if call == nil || call.worker != ident["worker"] || call.point != ident["point"] {
				t.Fatal("return without matching runtime call")
			}
			switch args["result"] {
			case "nil":
				call.result <- nil
			case "cancelled":
				if call.ctx.Err() == nil {
					t.Fatal("runtime context not canceled")
				}
			default:
				t.Fatal("unknown runtime return result")
			}
			<-call.returned
		case "cancel_context":
			if !fields(args, "invocation") && !(fields(args, "invocation scope") && args["scope"] == "invocation") {
				t.Fatal("unsupported cancellation scope")
			}
			id = str(args["invocation"])
			cancel := h.cancels[id]
			if cancel == nil {
				t.Fatal("unknown context")
			}
			cancel()
		default:
			t.Fatalf("unhandled local event: %s", s.Event)
		}
		reportCheck(t, row, base+"/local/"+s.Event, "internal/sut/external/vectors_test.go:vectorHarness.step")
	} else {
		t.Fatal("unknown target action")
	}
	synctest.Wait()
	for len(h.outputs) > 0 {
		h.pending = append(h.pending, <-h.outputs)
	}
	for len(h.p.client.calls) > 0 {
		call := <-h.p.client.calls
		if f.Type != "arrive" {
			t.Fatal("unexpected runtime call outside arrival receipt")
		}
		key := id + "/" + str(f.Body["arrival"])
		if h.calls[key] != nil {
			t.Fatal("duplicate runtime call")
		}
		h.calls[key] = call
		h.callCount++
	}
	for i, label := range s.Expect {
		h.observe(t, label, id, f, beforeSeq, beforeCalls)
		reportCheck(t, row, fmt.Sprintf("%s/expect/%d/%s", base, i, label), "internal/sut/external/vectors_test.go:vectorHarness.observe")
	}
}

func (h *vectorHarness) observe(t *testing.T, label, id string, f frame, beforeSeq, beforeCalls int) {
	t.Helper()
	a := h.p.a
	a.mu.Lock()
	defer a.mu.Unlock()
	w := a.invocations[id]
	need := func(ok bool) {
		t.Helper()
		if !ok {
			t.Fatalf("unmet vector assertion %s", label)
		}
	}
	result := func() sut.InvocationOutcome {
		if r, ok := h.outcomes[id]; ok {
			return r
		}
		select {
		case r, ok := <-h.streams[id]:
			need(ok)
			h.outcomes[id] = r
			return r
		default:
			t.Fatalf("result unavailable for %s", label)
			return sut.InvocationOutcome{}
		}
	}
	switch label {
	case "start_returns_handle":
		need(a.ready && !a.stopping)
		select {
		case err := <-h.start:
			need(err == nil)
		default:
			t.Fatal("Start still pending")
		}
	case "reserve_invocation":
		need(w != nil && !w.retired && a.workers[w.worker] == w)
	case "result_channel_created":
		need(h.streams[id] != nil)
	case "send_invoke", "send_release", "send_cancel":
		need(len(h.pending) > 0 && h.pending[0].Type == strings.TrimPrefix(label, "send_") && h.pending[0].Body["invocation"] == id)
	case "mark_accepted":
		need(w != nil && w.accepted && w.sending)
		select {
		case <-w.delivered:
		default:
			t.Fatal("accepted before write completion")
		}
	case "client_arrive_once":
		need(h.callCount == beforeCalls+1 && w != nil && w.bridges == 1)
	case "no_release_before_runtime_return":
		need(w != nil && w.outstanding && len(h.pending) == 0)
	case "worker_result_nil":
		r := result()
		need(r.Worker != nil && r.Unstarted == nil && r.Worker.Err == nil)
	case "worker_result_error", "worker_result_cancelled":
		r := result()
		need(r.Worker != nil && r.Worker.Err != nil)
		if label == "worker_result_cancelled" {
			need(errors.Is(r.Worker.Err, context.Canceled))
		}
	case "failure_class_error":
		need(orchestrator.ClassifyWorkerFailure(result().Worker.Err) == orchestrator.WorkerFailureError)
	case "failure_class_mysql_deadlock":
		need(orchestrator.ClassifyWorkerFailure(result().Worker.Err) == orchestrator.WorkerFailureMySQLDeadlock)
	case "close_result_channel":
		result()
		select {
		case _, ok := <-h.streams[id]:
			need(!ok)
		default:
			t.Fatal("result channel still open")
		}
	case "retire_invocation":
		need(w != nil && w.retired && w.bridges == 0 && a.workers[w.worker] != w)
	case "cancel_bridge":
		need(w != nil && w.ctx.Err() != nil && w.reason == "context")
	case "supplied_invocation_context_cancelled":
		need(h.contexts[id] != nil && h.contexts[id].Err() != nil)
	case "consume_cancelled_arrival":
		need(w != nil && w.reason != "" && !w.outstanding && a.received == f.Seq)
	case "no_client_arrive":
		need(h.callCount == beforeCalls)
	case "no_release", "no_reply":
		need(len(h.pending) == 0)
	case "drop_foreign_session":
		need(a.stale > 0 && f.Session != a.session)
	case "sequence_unchanged", "ignore_duplicate":
		need(a.received == beforeSeq)
	case "consume_retired_invocation":
		need(w != nil && w.retired && a.received == f.Seq)
	case "no_worker_result":
		for _, ch := range h.streams {
			need(len(ch) == 0)
		}
	case "no_new_worker_effect":
		need(w != nil && w.retired)
		next := a.workers[w.worker]
		need(next != nil && next != w && next.accepted && !next.retired && next.reason == "" && len(next.results) == 0)
	case "no_fatal":
		need(a.faults.Err() == nil)
	default:
		t.Fatalf("unhandled assertion %s", label)
	}
}
