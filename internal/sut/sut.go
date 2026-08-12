// Package sut defines the application adapter contract used by weavegate.
package sut

import (
	"context"
	"time"

	"github.com/weavegate/weavegate/internal/fixture"
)

// Adapter starts and stops a system-under-test integration.
type Adapter interface {
	Start(ctx context.Context, cfg SUTConfig, db *fixture.DB) (Handle, error)
	Stop(ctx context.Context) error
}

// Handle invokes registered worker commands on a started adapter.
type Handle interface {
	Invoke(ctx context.Context, workerID string, command string) (<-chan WorkerResult, error)
}

// SUTConfig selects a fixture variant and supplies its command parameters.
type SUTConfig struct {
	Variant string
	Params  map[string]string
}

// WorkerResult is the single terminal result of an asynchronous invocation.
type WorkerResult struct {
	WorkerID string
	Err      error
	Duration time.Duration
}
