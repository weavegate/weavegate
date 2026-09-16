package external

import (
	"context"
	"errors"
	"time"
)

func (a *adapter) watchExit() {
	err := a.proc.wait()
	a.mu.Lock()
	if err != nil {
		a.exitErr = errTransport
	}
	// During normal Stop, stdout may still contain the final stopped frame.
	// The cleanup join observes both EOF and exit without racing their delivery.
	if err != nil || !a.stopping {
		a.failLocked(errTransport, "transport", false)
	}
	close(a.exitDone)
	a.mu.Unlock()
}

func (a *adapter) Stop(ctx context.Context) error {
	a.mu.Lock()
	if !a.stopping {
		a.stopping = true
		now := a.now()
		a.stopDeadline = now.Add(a.opts.StopTimeout)
		if deadline, ok := ctx.Deadline(); ok && deadline.Before(a.stopDeadline) {
			a.stopDeadline = deadline
		}
		if ctx.Err() != nil {
			a.stopDeadline = now
		}
		a.graceDeadline = now.Add(a.stopDeadline.Sub(now) / 2)
		if !a.started {
			close(a.launchDone)
		}
		go a.cleanup()
	}
	a.mu.Unlock()
	// Prefer the latched result for completed cleanup, including later callers
	// with expired contexts. Pending callers cannot return nil on cancellation.
	select {
	case <-a.stopDone:
		return a.stopErr
	default:
	}
	select {
	case <-a.stopDone:
		return a.stopErr
	case <-ctx.Done():
		if fault := a.faults.Err(); fault != nil {
			return errors.Join(ctx.Err(), fault)
		}
		return ctx.Err()
	}
}

func (a *adapter) cleanup() {
	deadline := a.stopDeadline
	grace := time.NewTimer(time.Until(a.graceDeadline))
	defer grace.Stop()
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	if !waitUntil(ctx, a.launchDone) {
		a.finishStop(context.DeadlineExceeded)
		// A delayed launch still belongs to this adapter. Reap it when launch
		// returns; the expired Stop remains failed and never certifies cleanup.
		go func() {
			<-a.launchDone
			a.mu.Lock()
			p := a.proc
			a.mu.Unlock()
			if p != nil {
				p.closePipes()
				p.kill()
			}
		}()
		return
	}
	a.mu.Lock()
	p := a.proc
	if p == nil {
		err := error(a.faults.Err())
		if a.faults.Err() == nil {
			err = nil
		}
		a.mu.Unlock()
		a.finishStop(err)
		return
	}
	// Startup may have exhausted its budget before the first write. Never send
	// Stop as the first frame to an uninitialized peer.
	initialized := a.startSent
	workers := make([]<-chan struct{}, 0, len(a.workers))
	for _, w := range a.workers {
		a.cancelLocked(w, "stop")
		workers = append(workers, w.done)
	}
	if initialized {
		a.enqueueLocked("stop", map[string]any{"budget_ms": 1}, nil)
	}
	a.mu.Unlock()

	// Joining these observations does not perform their effects: the reader
	// observes EOF, the process watcher alone reaps, and bridge tasks unwind on
	// cancellation before closing the invocation streams.
	graceDone := make(chan struct{})
	joinCtx, stopJoin := context.WithCancel(ctx)
	defer stopJoin()
	go func() {
		for _, ch := range append([]<-chan struct{}{a.readDone, a.exitDone, a.stderrDone}, workers...) {
			if !waitUntil(joinCtx, ch) {
				return
			}
		}
		close(graceDone)
	}()
	normal := false
	if initialized {
		select {
		case <-graceDone:
			a.mu.Lock()
			normal = a.stopped && a.eof && a.exitErr == nil && a.faults.Err() == nil && len(a.workers) == 0
			a.mu.Unlock()
		case <-grace.C:
		case <-ctx.Done():
		}
	}
	a.mu.Lock()
	if !normal {
		cause := error(context.DeadlineExceeded)
		if !initialized {
			cause = errors.New("external SUT stopped before initialization")
		}
		a.failLocked(cause, "shutdown", false)
	}
	a.writerCancel()
	a.mu.Unlock()
	if !normal {
		p.kill()
	}
	p.closePipes()
	for _, ch := range append([]<-chan struct{}{a.readDone, a.writeDone, a.stderrDone, a.exitDone}, workers...) {
		if !waitUntil(ctx, ch) {
			a.finishStop(context.DeadlineExceeded)
			return
		}
	}
	a.finishStop(nil)
}

func waitUntil(ctx context.Context, ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
	}
	select {
	case <-ch:
		return true
	case <-ctx.Done():
		return false
	}
}

func (a *adapter) finishStop(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if fault := a.faults.Err(); fault != nil {
		err = errors.Join(err, fault)
	}
	a.stopErr = err
	close(a.stopDone)
}
