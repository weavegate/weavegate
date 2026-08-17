// Command weavegate reaches a verdict on a configured concurrent workflow
// and saves the run evidence to disk.
package main

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

func main() {
	ctx, stop := newProcessContext(context.Background(), signal.Notify, signal.Stop)
	code := ExecuteContext(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

type notifySignals func(chan<- os.Signal, ...os.Signal)
type stopSignals func(chan<- os.Signal)

func newProcessContext(
	parent context.Context,
	notify notifySignals,
	stopNotify stopSignals,
) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	signals := make(chan os.Signal, 1)
	notify(signals, os.Interrupt, syscall.SIGTERM)

	var once sync.Once
	stop := func() {
		once.Do(func() {
			// Restore the default disposition before cancellation starts the
			// detached cleanup path, so another signal remains an escape hatch.
			stopNotify(signals)
			cancel()
		})
	}
	go func() {
		select {
		case <-signals:
			stop()
		case <-ctx.Done():
			stop()
		}
	}()

	return ctx, stop
}
