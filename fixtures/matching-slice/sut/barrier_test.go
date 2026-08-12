package matchingsut

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

type barrierReleaseReason string

const (
	barrierAllArrived  barrierReleaseReason = "all_arrived"
	barrierDBBlocked   barrierReleaseReason = "db_blocked"
	barrierTestCleanup barrierReleaseReason = "test_cleanup"
)

type testBarrier struct {
	mu sync.Mutex

	point       string
	released    chan struct{}
	changed     chan struct{}
	release     barrierReleaseReason
	events      []syncPointEvent
	arrivals    []string
	releasedIDs []string
}

func newTestBarrier(point string) *testBarrier {
	return &testBarrier{
		point:    point,
		released: make(chan struct{}),
		changed:  make(chan struct{}),
	}
}

func (b *testBarrier) Arrive(ctx context.Context, workerID string, point string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	b.mu.Lock()
	b.events = append(b.events, syncPointEvent{workerID: workerID, point: point})
	if point != b.point {
		b.mu.Unlock()
		return nil
	}

	b.arrivals = append(b.arrivals, workerID)
	b.signalChangedLocked()
	if b.release != "" {
		b.mu.Unlock()
		return nil
	}
	released := b.released
	b.mu.Unlock()

	select {
	case <-released:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *testBarrier) waitForArrivals(ctx context.Context, workerIDs ...string) error {
	for {
		b.mu.Lock()
		if containsWorkerIDs(b.arrivals, workerIDs) {
			b.mu.Unlock()
			return nil
		}
		changed := b.changed
		b.mu.Unlock()

		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func containsWorkerIDs(arrivals []string, workerIDs []string) bool {
	seen := make(map[string]struct{}, len(arrivals))
	for _, workerID := range arrivals {
		seen[workerID] = struct{}{}
	}
	for _, workerID := range workerIDs {
		if _, ok := seen[workerID]; !ok {
			return false
		}
	}
	return true
}

func (b *testBarrier) Release(reason barrierReleaseReason) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.release != "" {
		return
	}
	b.release = reason
	b.releasedIDs = append([]string(nil), b.arrivals...)
	close(b.released)
	b.signalChangedLocked()
}

func (b *testBarrier) signalChangedLocked() {
	close(b.changed)
	b.changed = make(chan struct{})
}

func (b *testBarrier) observation() barrierObservation {
	b.mu.Lock()
	defer b.mu.Unlock()

	return barrierObservation{
		release:     b.release,
		events:      append([]syncPointEvent(nil), b.events...),
		arrivals:    append([]string(nil), b.arrivals...),
		releasedIDs: append([]string(nil), b.releasedIDs...),
	}
}

type barrierObservation struct {
	release     barrierReleaseReason
	events      []syncPointEvent
	arrivals    []string
	releasedIDs []string
}

func TestBarrier(t *testing.T) {
	t.Run("waits for explicit release", func(t *testing.T) {
		barrier := newTestBarrier(BeforeInsertAssignment)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		errCh := make(chan error, 2)
		for _, workerID := range []string{"w1", "w2"} {
			go func(workerID string) {
				errCh <- barrier.Arrive(ctx, workerID, BeforeInsertAssignment)
			}(workerID)
		}
		if err := barrier.waitForArrivals(ctx, "w1", "w2"); err != nil {
			t.Fatalf("wait for barrier arrivals: %v", err)
		}
		if got := barrier.observation().release; got != "" {
			t.Fatalf("barrier released without an explicit signal: %s", got)
		}

		barrier.Release(barrierAllArrived)
		for range 2 {
			if err := <-errCh; err != nil {
				t.Fatalf("barrier arrival after release: %v", err)
			}
		}
		observation := barrier.observation()
		if observation.release != barrierAllArrived || len(observation.releasedIDs) != 2 {
			t.Fatalf("released barrier = %s, want all_arrived with two workers", observation)
		}
	})

	t.Run("passes arrivals after release", func(t *testing.T) {
		barrier := newTestBarrier(BeforeInsertAssignment)
		barrier.Release(barrierDBBlocked)

		if err := barrier.Arrive(context.Background(), "w2", BeforeInsertAssignment); err != nil {
			t.Fatalf("late barrier arrival: %v", err)
		}
		observation := barrier.observation()
		if len(observation.arrivals) != 1 || len(observation.releasedIDs) != 0 {
			t.Fatalf("barrier after late arrival = %s", observation)
		}
	})

	t.Run("returns context cancellation while waiting", func(t *testing.T) {
		barrier := newTestBarrier(BeforeInsertAssignment)
		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() {
			errCh <- barrier.Arrive(ctx, "w1", BeforeInsertAssignment)
		}()

		if err := barrier.waitForArrivals(context.Background(), "w1"); err != nil {
			t.Fatalf("wait for first barrier arrival: %v", err)
		}
		cancel()

		if err := <-errCh; !errors.Is(err, context.Canceled) {
			t.Fatalf("barrier cancellation error = %v, want context.Canceled", err)
		}
	})
}

func (o barrierObservation) String() string {
	return fmt.Sprintf(
		"release=%s arrivals=%v released_ids=%v events=%v",
		o.release,
		o.arrivals,
		o.releasedIDs,
		o.events,
	)
}
