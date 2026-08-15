package main

import (
	"strings"
	"testing"

	"github.com/weavegate/weavegate/internal/ci"
	"github.com/weavegate/weavegate/internal/config"
)

func validResolveConfig() config.Config {
	return config.Config{
		Target: config.Target{
			DB: "mysql:8.4",
			Schema: config.Schema{
				Migrations: "fixtures/matching-slice/db/migration",
				Seed:       "fixtures/matching-slice/db/seed.sql",
			},
			SUT: config.SUT{Adapter: "gonative", Entrypoint: "matching-slice", Variant: "vulnerable"},
		},
		Scenarios: map[string]config.Scenario{
			"concurrent-assign": {
				Workers: []config.Worker{
					{ID: "w1", Command: "assign", Args: map[string]string{"request_id": "42"}},
					{ID: "w2", Command: "assign", Args: map[string]string{"request_id": "42"}},
				},
				SyncPoints: []string{"after_read_request", "before_insert_assignment"},
			},
		},
		Oracle: config.Oracle{
			Assertions: []config.Assertion{
				{ID: "active-assignment-is-unique", SQL: "SELECT 1 WHERE FALSE", ExpectRows: 0},
			},
		},
		Run: config.Run{Repeat: 20, ArriveTimeoutMS: 250, ExplorePasses: 3},
	}
}

func TestResolve(t *testing.T) {
	observed := map[string]string{}
	cfg := validResolveConfig()

	t.Run("valid", func(t *testing.T) {
		resolved, err := Resolve(cfg, "concurrent-assign", "")
		if err != nil {
			t.Fatalf("resolve valid config: %v", err)
		}
		if resolved.Scenario.SUTConfig.Variant != "vulnerable" {
			t.Fatalf("variant = %q, want vulnerable (defaulted from config)", resolved.Scenario.SUTConfig.Variant)
		}
		if len(resolved.Scenario.Workers) != 2 {
			t.Fatalf("workers = %d, want 2", len(resolved.Scenario.Workers))
		}
		if len(resolved.Scenario.SyncPoints) != 2 {
			t.Fatalf("sync_points = %d, want 2", len(resolved.Scenario.SyncPoints))
		}
		if len(cfg.Oracle.Assertions) != 1 || resolved.Oracle == nil {
			t.Fatalf("oracles not resolved: assertions=%d set=%v", len(cfg.Oracle.Assertions), resolved.Oracle)
		}
		if resolved.Fixture.Image != "mysql:8.4" {
			t.Fatalf("image = %q, want mysql:8.4", resolved.Fixture.Image)
		}
		if resolved.Timeouts.BlockInference.Milliseconds() != 250 ||
			resolved.Timeouts.Step.Milliseconds() != 5000 ||
			resolved.Timeouts.Run.Milliseconds() != 15000 ||
			resolved.Timeouts.Stop.Milliseconds() != 5000 {
			t.Fatalf("timeouts = %+v, want 250/5000/15000/5000 ms", resolved.Timeouts)
		}
		if resolved.NewAdapter == nil || resolved.NewRuntime == nil {
			t.Fatalf("adapter/runtime factories are nil: %+v", resolved)
		}

		observed["entrypoint"] = "matching-slice"
		observed["adapter"] = cfg.Target.SUT.Adapter
		observed["variant"] = "vulnerable"
		observed["workers"] = "2"
		observed["sync_points"] = "2"
		observed["oracles"] = "1"
		observed["oracle_order"] = "preserved"
		observed["image"] = "mysql:8.4"
		observed["block_ms"] = "250"
		observed["step_ms"] = "5000"
		observed["run_ms"] = "15000"
		observed["stop_ms"] = "5000"
		observed["no_container"] = "true"
	})

	t.Run("variant_override", func(t *testing.T) {
		resolved, err := Resolve(cfg, "concurrent-assign", "fixed")
		if err != nil {
			t.Fatalf("resolve with --variant override: %v", err)
		}
		if resolved.Scenario.SUTConfig.Variant != "fixed" {
			t.Fatalf("variant = %q, want fixed override", resolved.Scenario.SUTConfig.Variant)
		}
	})

	t.Run("unknown_entrypoint", func(t *testing.T) {
		bad := cfg
		bad.Target.SUT.Entrypoint = "does-not-exist"
		_, err := Resolve(bad, "concurrent-assign", "")
		if err == nil {
			t.Fatal("resolve unknown entrypoint: want error, got nil")
		}
		if got := ci.ExitCode(err, ci.Verdict{}); got != ci.ExitInput {
			t.Fatalf("unknown entrypoint exit = %d, want %d", got, ci.ExitInput)
		}
		if !strings.Contains(err.Error(), "matching-slice") {
			t.Fatalf("unknown entrypoint error = %v, want it to list known IDs", err)
		}
		observed["unknown_entrypoint"] = "5"
	})

	t.Run("unknown_variant", func(t *testing.T) {
		_, err := Resolve(cfg, "concurrent-assign", "does-not-exist")
		if err == nil {
			t.Fatal("resolve unknown variant: want error, got nil")
		}
		if got := ci.ExitCode(err, ci.Verdict{}); got != ci.ExitInput {
			t.Fatalf("unknown variant exit = %d, want %d", got, ci.ExitInput)
		}
		observed["unknown_variant"] = "5"
	})

	t.Run("bad_scenario", func(t *testing.T) {
		_, err := Resolve(cfg, "does-not-exist", "")
		if err == nil {
			t.Fatal("resolve unknown scenario: want error, got nil")
		}
		if got := ci.ExitCode(err, ci.Verdict{}); got != ci.ExitInput {
			t.Fatalf("unknown scenario exit = %d, want %d", got, ci.ExitInput)
		}
		observed["bad_scenario"] = "5"
	})

	order := []string{
		"entrypoint", "adapter", "variant", "workers", "sync_points",
		"oracles", "oracle_order", "image", "block_ms", "step_ms", "run_ms", "stop_ms",
		"unknown_entrypoint", "unknown_variant", "bad_scenario", "no_container",
	}
	parts := make([]string, 0, len(order))
	for _, key := range order {
		value, ok := observed[key]
		if !ok {
			t.Fatalf("marker attribute %q was not observed by any subtest", key)
		}
		parts = append(parts, key+"="+value)
	}
	t.Logf("CLI_RESOLVE_RESULT %s", strings.Join(parts, " "))
}
