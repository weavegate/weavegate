package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadValidBytes(t *testing.T) string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("testdata", "valid.yaml"))
	if err != nil {
		t.Fatalf("read valid.yaml: %v", err)
	}
	return string(raw)
}

// mutate replaces exactly one occurrence of old with new and fails the test
// if old does not appear exactly once, so a mutation can never silently
// become a no-op.
func mutate(t *testing.T, source, old, new string) string {
	t.Helper()

	count := strings.Count(source, old)
	if count != 1 {
		t.Fatalf("mutate: %q occurs %d times in source, want 1", old, count)
	}
	return strings.Replace(source, old, new, 1)
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestConfigLoad(t *testing.T) {
	base := loadValidBytes(t)
	observed := map[string]string{}

	t.Run("valid", func(t *testing.T) {
		cfg, err := Load(writeConfig(t, base))
		if err != nil {
			t.Fatalf("load valid config: %v", err)
		}
		if len(cfg.Scenarios) != 1 {
			t.Fatalf("scenarios = %d, want 1", len(cfg.Scenarios))
		}
		scenario, ok := cfg.Scenarios["concurrent-assign"]
		if !ok {
			t.Fatalf("scenario %q is missing", "concurrent-assign")
		}
		if len(scenario.Workers) != 2 {
			t.Fatalf("workers = %d, want 2", len(scenario.Workers))
		}
		if len(scenario.SyncPoints) != 2 {
			t.Fatalf("sync_points = %d, want 2", len(scenario.SyncPoints))
		}
		if len(cfg.Oracle.Assertions) != 1 {
			t.Fatalf("assertions = %d, want 1", len(cfg.Oracle.Assertions))
		}
		if cfg.Run.Repeat != DefaultRepeat {
			t.Fatalf("repeat = %d, want default %d", cfg.Run.Repeat, DefaultRepeat)
		}
		if cfg.Run.ArriveTimeoutMS != 250 {
			t.Fatalf("arrive_timeout_ms = %d, want explicit 250", cfg.Run.ArriveTimeoutMS)
		}
		if cfg.Run.ExplorePasses != DefaultExplorePasses {
			t.Fatalf("explore_passes = %d, want default %d", cfg.Run.ExplorePasses, DefaultExplorePasses)
		}
		if !filepath.IsAbs(cfg.Target.Schema.Migrations) || !filepath.IsAbs(cfg.Target.Schema.Seed) {
			t.Fatalf(
				"schema paths not resolved to absolute: migrations=%q seed=%q",
				cfg.Target.Schema.Migrations,
				cfg.Target.Schema.Seed,
			)
		}

		observed["scenarios"] = "1"
		observed["workers"] = "2"
		observed["sync_points"] = "2"
		observed["assertions"] = "1"
		observed["repeat"] = "20"
		observed["arrive_timeout_ms"] = "250"
		observed["explore_passes"] = "3"
		observed["defaults"] = "applied"
		observed["relative_paths"] = "resolved"
	})

	t.Run("unknown_key", func(t *testing.T) {
		content := mutate(t, base, "run:\n  arrive_timeout_ms: 250\n", "run:\n  arrive_timeout_ms: 250\nbogus_top_level_key: true\n")
		if _, err := Load(writeConfig(t, content)); err == nil {
			t.Fatal("load config with unknown key: want error, got nil")
		}
		observed["unknown_key"] = "error"
	})

	t.Run("report_section", func(t *testing.T) {
		content := base + "report:\n  formats: [json]\n"
		if _, err := Load(writeConfig(t, content)); err == nil {
			t.Fatal("load config with report section: want error, got nil")
		}
		observed["report_section"] = "error"
	})

	t.Run("missing_required", func(t *testing.T) {
		cases := map[string]struct{ old, new string }{
			"target.db":              {"  db: mysql:8.4\n", ""},
			"schema.migrations":      {"    migrations: ../fixtures/matching-slice/db/migration\n", ""},
			"schema.seed":            {"    seed: ../fixtures/matching-slice/db/seed.sql\n", ""},
			"sut.adapter":            {"    adapter: gonative\n", ""},
			"sut.entrypoint":         {"    entrypoint: matching-slice\n", ""},
			"sut.variant":            {"    variant: vulnerable\n", ""},
			"scenarios[].workers":    {"    workers:\n      - id: A\n        command: assign\n        args:\n          request_id: \"42\"\n      - id: B\n        command: assign\n        args:\n          request_id: \"42\"\n", "    workers: []\n"},
			"scenarios[].syncpoints": {"    sync_points:\n      - after_read_request\n      - before_insert_assignment\n", "    sync_points: []\n"},
			"oracle.assertions":      {"oracle:\n  assertions:\n    - id: active-assignment-is-unique\n      sql: |\n        SELECT project_request_id FROM assignment\n        WHERE status = 'ACTIVE' GROUP BY project_request_id HAVING COUNT(*) > 1\n      expect_rows: 0\n", "oracle:\n  assertions: []\n"},
		}
		for name, mutation := range cases {
			t.Run(name, func(t *testing.T) {
				content := mutate(t, base, mutation.old, mutation.new)
				if _, err := Load(writeConfig(t, content)); err == nil {
					t.Fatalf("load config missing %s: want error, got nil", name)
				}
			})
		}
		observed["missing_required"] = "error"
	})

	t.Run("non_mysql_db", func(t *testing.T) {
		content := mutate(t, base, "db: mysql:8.4", "db: postgres:14")
		if _, err := Load(writeConfig(t, content)); err == nil {
			t.Fatal("load config with non-MySQL db: want error, got nil")
		}
		observed["non_mysql_db"] = "error"
	})

	t.Run("path_entrypoint", func(t *testing.T) {
		content := mutate(t, base, "entrypoint: matching-slice", "entrypoint: ./fixtures/matching-slice")
		if _, err := Load(writeConfig(t, content)); err == nil {
			t.Fatal("load config with path-shaped entrypoint: want error, got nil")
		}
		observed["path_entrypoint"] = "error"
	})

	t.Run("bad_expect_rows", func(t *testing.T) {
		content := mutate(t, base, "expect_rows: 0", "expect_rows: 1")
		if _, err := Load(writeConfig(t, content)); err == nil {
			t.Fatal("load config with non-zero expect_rows: want error, got nil")
		}
		observed["bad_expect_rows"] = "error"
	})

	t.Run("bad_assertion_id", func(t *testing.T) {
		content := mutate(t, base, "id: active-assignment-is-unique", "id: Active_Assignment!")
		if _, err := Load(writeConfig(t, content)); err == nil {
			t.Fatal("load config with invalid assertion id: want error, got nil")
		}
		observed["bad_assertion_id"] = "error"
	})

	t.Run("duplicate_worker", func(t *testing.T) {
		content := mutate(t, base, "      - id: B\n        command: assign", "      - id: A\n        command: assign")
		if _, err := Load(writeConfig(t, content)); err == nil {
			t.Fatal("load config with duplicate worker id: want error, got nil")
		}
		observed["duplicate_worker"] = "error"
	})

	t.Run("duplicate_sync_point", func(t *testing.T) {
		content := mutate(t, base, "      - before_insert_assignment", "      - after_read_request")
		if _, err := Load(writeConfig(t, content)); err == nil {
			t.Fatal("load config with duplicate sync-point: want error, got nil")
		}
		observed["duplicate_sync_point"] = "error"
	})

	t.Run("nonpositive_run_value", func(t *testing.T) {
		content := mutate(t, base, "arrive_timeout_ms: 250", "arrive_timeout_ms: -5")
		if _, err := Load(writeConfig(t, content)); err == nil {
			t.Fatal("load config with negative run value: want error, got nil")
		}
		observed["nonpositive_run_value"] = "error"
	})

	// An explicit 0 must be rejected the same as any other nonpositive
	// value, not silently overwritten by the default the way an omitted
	// key is (the two are otherwise indistinguishable after YAML decoding
	// into a plain int).
	t.Run("explicit_zero_run_value", func(t *testing.T) {
		cases := map[string]struct{ old, new string }{
			"repeat":            {"run:\n  arrive_timeout_ms: 250\n", "run:\n  arrive_timeout_ms: 250\n  repeat: 0\n"},
			"arrive_timeout_ms": {"arrive_timeout_ms: 250", "arrive_timeout_ms: 0"},
			"explore_passes":    {"run:\n  arrive_timeout_ms: 250\n", "run:\n  arrive_timeout_ms: 250\n  explore_passes: 0\n"},
		}
		for name, mutation := range cases {
			t.Run(name, func(t *testing.T) {
				content := mutate(t, base, mutation.old, mutation.new)
				if _, err := Load(writeConfig(t, content)); err == nil {
					t.Fatalf("load config with explicit run.%s: 0: want error, got nil", name)
				}
			})
		}
		observed["explicit_zero_run_value"] = "error"
	})

	t.Run("worker_args_mismatch", func(t *testing.T) {
		content := mutate(
			t,
			base,
			"      - id: B\n        command: assign\n        args:\n          request_id: \"42\"\n",
			"      - id: B\n        command: assign\n        args:\n          request_id: \"43\"\n",
		)
		if _, err := Load(writeConfig(t, content)); err == nil {
			t.Fatal("load config with mismatched worker args: want error, got nil")
		}
		observed["worker_args_mismatch"] = "error"
	})

	t.Run("unsupported_adapter", func(t *testing.T) {
		content := mutate(t, base, "adapter: gonative", "adapter: springtest")
		if _, err := Load(writeConfig(t, content)); err == nil {
			t.Fatal("load config with unsupported adapter: want error, got nil")
		}
		observed["unsupported_adapter"] = "error"
	})

	order := []string{
		"scenarios", "workers", "sync_points", "assertions",
		"repeat", "arrive_timeout_ms", "explore_passes",
		"defaults", "relative_paths",
		"unknown_key", "report_section", "missing_required",
		"non_mysql_db", "path_entrypoint", "bad_expect_rows",
		"bad_assertion_id", "duplicate_worker", "duplicate_sync_point",
		"nonpositive_run_value", "explicit_zero_run_value", "worker_args_mismatch", "unsupported_adapter",
	}
	parts := make([]string, 0, len(order))
	for _, key := range order {
		value, ok := observed[key]
		if !ok {
			t.Fatalf("marker attribute %q was not observed by any subtest", key)
		}
		parts = append(parts, key+"="+value)
	}
	t.Logf("CONFIG_LOAD_RESULT %s", strings.Join(parts, " "))
}
