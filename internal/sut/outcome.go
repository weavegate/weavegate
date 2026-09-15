package sut

import (
	"fmt"
	"sync"
)

// InvocationOutcome is exactly one terminal invocation outcome. The producer
// closes the stream after publishing it and returning all invocation resources.
// Session failure with unknown outcome closes the stream without an outcome,
// after latching the fault; it must never manufacture a WorkerResult.
type InvocationOutcome struct {
	Worker    *WorkerResult
	Unstarted *UnstartedResult
}

// UnstartedResult proves no command transaction began and all acquired resources
// were returned. Err is the non-nil cause. Unknown/failed cleanup is a session
// fault, not an UnstartedResult. This outcome never authorizes runtime Finish.
type UnstartedResult struct {
	WorkerID string
	Err      error
}

func (r *UnstartedResult) Error() string {
	return fmt.Sprintf("worker %q did not start: %v", r.WorkerID, r.Err)
}

func (r *UnstartedResult) Unwrap() error { return r.Err }

// SessionFault is an adapter-wide failure, independent of worker outcomes.
// Cause is retained for errors.Is/errors.As and must be non-nil.
type SessionFault struct {
	Cause error
}

func (f *SessionFault) Error() string { return fmt.Sprintf("SUT session fault: %v", f.Cause) }
func (f *SessionFault) Unwrap() error { return f.Cause }

// SessionFaults is a latched, read-only fault view that stays valid through Stop.
// Done closes on the first fault; Err returns that same fault to every observer.
// A healthy session leaves Done open, including after a successful Stop.
type SessionFaults interface {
	Done() <-chan struct{}
	Err() *SessionFault
}

// FaultLatch implements SessionFaults. The zero value is ready to use. Do not
// copy a latch after first use. Adapters must publish all faults before a
// successful Stop returns and must not mutate a published fault or cause.
type FaultLatch struct {
	mu    sync.Mutex
	done  chan struct{}
	fault *SessionFault
}

func (l *FaultLatch) Done() <-chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.initLocked()
	return l.done
}

func (l *FaultLatch) Err() *SessionFault {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fault
}

// Fail latches the first non-nil cause. Later calls preserve the original fault.
func (l *FaultLatch) Fail(cause error) {
	if cause == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fault == nil {
		l.initLocked()
		l.fault = &SessionFault{Cause: cause}
		close(l.done)
	}
}

func (l *FaultLatch) initLocked() {
	if l.done == nil {
		l.done = make(chan struct{})
	}
}
