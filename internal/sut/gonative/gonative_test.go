package gonative

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/weavegate/weavegate/internal/fixture"
	"github.com/weavegate/weavegate/internal/sut"
)

func TestGoNativeRegistry(t *testing.T) {
	t.Parallel()

	registry := staticRegistry{
		"assign": func(context.Context, string, *sql.Conn) error {
			return nil
		},
	}

	t.Run("rejects invocation before start", func(t *testing.T) {
		adapter := New(registry).(*adapter)
		_, err := adapter.Invoke(context.Background(), "w1", "assign")
		assertErrorContains(t, err, "not-started")
	})

	t.Run("rejects a missing database", func(t *testing.T) {
		adapter := New(registry)
		_, err := adapter.Start(context.Background(), sut.SUTConfig{}, nil)
		assertErrorContains(t, err, "database is required")

		adapter = New(registry)
		_, err = adapter.Start(context.Background(), sut.SUTConfig{}, &fixture.DB{})
		assertErrorContains(t, err, "database is required")
	})

	t.Run("enforces the started lifecycle", func(t *testing.T) {
		adapter := New(registry).(*adapter)
		db := &fixture.DB{SQL: new(sql.DB)}
		handle, err := adapter.Start(context.Background(), sut.SUTConfig{}, db)
		if err != nil {
			t.Fatalf("start adapter: %v", err)
		}
		if got := len(adapter.commands); got != 1 {
			t.Fatalf("registered commands = %d, want 1", got)
		}

		if _, err := adapter.Start(context.Background(), sut.SUTConfig{}, db); err == nil {
			t.Fatal("second Start returned nil, want error")
		}
		_, err = handle.Invoke(context.Background(), "w1", "unknown")
		assertErrorContains(t, err, "command is not registered")

		if err := adapter.Stop(context.Background()); err != nil {
			t.Fatalf("first Stop: %v", err)
		}
		if err := adapter.Stop(context.Background()); err != nil {
			t.Fatalf("second Stop: %v", err)
		}
		_, err = handle.Invoke(context.Background(), "w1", "assign")
		assertErrorContains(t, err, "stopped")
	})

	t.Log("SUT_REGISTRY_RESULT commands=1 unknown_command=error missing_db=error stop_idempotent=true")
}

type staticRegistry map[string]CommandFunc

func (r staticRegistry) Commands(sut.SUTConfig) (map[string]CommandFunc, error) {
	return r, nil
}

func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want substring %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want substring %q", err, want)
	}
}
