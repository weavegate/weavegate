package matchingsut

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type barrierReleaseReason string

const (
	barrierAllArrived barrierReleaseReason = "all_arrived"
	barrierTimeout    barrierReleaseReason = "timeout"
)

type testBarrier struct {
	mu sync.Mutex

	point        string
	participants int
	timeout      time.Duration
	released     chan struct{}
	timer        *time.Timer
	release      barrierReleaseReason
	events       []syncPointEvent
	arrivals     []string
	releasedIDs  []string
}

func newTestBarrier(point string, participants int, timeout time.Duration) *testBarrier {
	return &testBarrier{
		point:        point,
		participants: participants,
		timeout:      timeout,
		released:     make(chan struct{}),
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
	if b.release != "" {
		b.mu.Unlock()
		return nil
	}

	if len(b.arrivals) == 1 {
		b.timer = time.AfterFunc(b.timeout, func() {
			b.releaseWaiting(barrierTimeout)
		})
	}
	if len(b.arrivals) >= b.participants {
		b.releaseLocked(barrierAllArrived)
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

func (b *testBarrier) releaseWaiting(reason barrierReleaseReason) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.releaseLocked(reason)
}

func (b *testBarrier) releaseLocked(reason barrierReleaseReason) {
	if b.release != "" {
		return
	}
	if reason == barrierAllArrived && b.timer != nil {
		b.timer.Stop()
	}
	b.release = reason
	b.releasedIDs = append([]string(nil), b.arrivals...)
	close(b.released)
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
	t.Run("passes arrivals after timeout", func(t *testing.T) {
		barrier := newTestBarrier(BeforeInsertAssignment, 2, 10*time.Millisecond)
		if err := barrier.Arrive(context.Background(), "w1", BeforeInsertAssignment); err != nil {
			t.Fatalf("first barrier arrival: %v", err)
		}

		beforeLateArrival := barrier.observation()
		if beforeLateArrival.release != barrierTimeout {
			t.Fatalf("barrier before late arrival = %s, want timeout", beforeLateArrival)
		}
		if len(beforeLateArrival.releasedIDs) != 1 || beforeLateArrival.releasedIDs[0] != "w1" {
			t.Fatalf("released worker IDs = %v, want [w1]", beforeLateArrival.releasedIDs)
		}

		if err := barrier.Arrive(context.Background(), "w2", BeforeInsertAssignment); err != nil {
			t.Fatalf("late barrier arrival: %v", err)
		}
		afterLateArrival := barrier.observation()
		if len(afterLateArrival.arrivals) != 2 || len(afterLateArrival.releasedIDs) != 1 {
			t.Fatalf("barrier after late arrival = %s", afterLateArrival)
		}
	})

	t.Run("returns context cancellation while waiting", func(t *testing.T) {
		barrier := newTestBarrier(BeforeInsertAssignment, 2, time.Minute)
		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() {
			errCh <- barrier.Arrive(ctx, "w1", BeforeInsertAssignment)
		}()

		deadline := time.Now().Add(time.Second)
		for len(barrier.observation().arrivals) == 0 {
			if time.Now().After(deadline) {
				t.Fatal("worker did not reach barrier before cancellation")
			}
			time.Sleep(time.Millisecond)
		}
		cancel()

		select {
		case err := <-errCh:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("barrier cancellation error = %v, want context.Canceled", err)
			}
		case <-time.After(time.Second):
			t.Fatal("barrier did not return after context cancellation")
		}

		if err := barrier.Arrive(context.Background(), "w2", BeforeInsertAssignment); err != nil {
			t.Fatalf("release cancelled barrier waiter: %v", err)
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
