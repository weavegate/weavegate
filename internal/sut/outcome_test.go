package sut

import (
	"errors"
	"sync"
	"testing"
)

func TestFaultLatch(t *testing.T) {
	var latch FaultLatch
	cause := errors.New("first failure")
	if latch.Err() != nil {
		t.Fatal("new latch is faulted")
	}
	latch.Fail(nil)
	select {
	case <-latch.Done():
		t.Fatal("nil failure closed latch")
	default:
	}
	var observers sync.WaitGroup
	for range 16 {
		observers.Add(1)
		go func() {
			defer observers.Done()
			<-latch.Done()
			if !errors.Is(latch.Err(), cause) {
				t.Errorf("cause = %v", latch.Err())
			}
		}()
	}
	latch.Fail(cause)
	first := latch.Err()
	latch.Fail(errors.New("later failure"))
	observers.Wait()
	<-latch.Done()
	if first != latch.Err() {
		t.Fatal("fault changed for late observer")
	}
	t.Log("SUT_FAULT_LATCH_RESULT first_cause=retained observers=all late_observer=notified")
}
