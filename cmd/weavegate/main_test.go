package main

import (
	"context"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestSignalContextStopsNotificationBeforeCancel(t *testing.T) {
	var notified chan<- os.Signal
	var stopped atomic.Bool
	ctx, stop := newProcessContext(
		context.Background(),
		func(ch chan<- os.Signal, signals ...os.Signal) {
			notified = ch
			if len(signals) != 2 || signals[0] != os.Interrupt || signals[1] != syscall.SIGTERM {
				t.Fatalf("registered signals = %v, want SIGINT and SIGTERM", signals)
			}
		},
		func(ch chan<- os.Signal) {
			if ch != notified {
				t.Fatal("stopped a different signal channel")
			}
			stopped.Store(true)
		},
	)
	t.Cleanup(stop)

	notified <- os.Interrupt
	select {
	case <-ctx.Done():
		if !stopped.Load() {
			t.Fatal("context canceled before signal notification stopped")
		}
	case <-time.After(time.Second):
		t.Fatal("first signal did not cancel the process context")
	}
}

func TestSignalContextStopsNotificationOnNormalReturn(t *testing.T) {
	stopCalls := 0
	ctx, stop := newProcessContext(
		context.Background(),
		func(chan<- os.Signal, ...os.Signal) {},
		func(chan<- os.Signal) { stopCalls++ },
	)

	stop()
	stop()
	if stopCalls != 1 {
		t.Fatalf("stop notification calls = %d, want 1", stopCalls)
	}
	if ctx.Err() != context.Canceled {
		t.Fatalf("normal-return stop context error = %v, want %v", ctx.Err(), context.Canceled)
	}
}
