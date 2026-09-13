// Package sut defines the application adapter contract used by weavegate.
package sut

import (
	"context"
	"time"

	"github.com/weavegate/weavegate/internal/fixture"
)

// Adapter starts and stops a system-under-test integration. Successful Stop
// completes invocation resource cleanup, stream closure, and fault publication.
// Stop must honor its context; an error does not prove cleanup completed.
type Adapter interface {
	Start(ctx context.Context, cfg SUTConfig, db *fixture.DB) (Handle, error)
	Stop(ctx context.Context) error
}

// Handle invokes registered worker commands on a started adapter. A worker ID
// is unique among active invocations and can be reused after the previous
// invocation's terminal result channel has closed.
type Handle interface {
	Invoke(ctx context.Context, workerID string, command string) (<-chan InvocationOutcome, error)
	Faults() SessionFaults
}

// SUTConfig selects a fixture variant and supplies its command parameters.
type SUTConfig struct {
	Variant string
	Params  map[string]string
}

// WorkerResult is the terminal result of a command that started.
// An adapter may publish it only after the worker command has committed or
// rolled back and its worker-owned database connection has been returned.
type WorkerResult struct {
	WorkerID string
	Err      error
	Duration time.Duration
}
