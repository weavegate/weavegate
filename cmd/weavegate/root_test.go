package main

import (
	"bytes"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/weavegate/weavegate/internal/ci"
)

func run(args ...string) (stdout, stderr string, exitCode int) {
	var outBuf, errBuf bytes.Buffer
	exitCode = Execute(args, &outBuf, &errBuf)
	return outBuf.String(), errBuf.String(), exitCode
}

func TestReportedVersion(t *testing.T) {
	originalVersion, originalReadBuildInfo := version, readBuildInfo
	t.Cleanup(func() {
		version = originalVersion
		readBuildInfo = originalReadBuildInfo
	})

	t.Run("linker value wins", func(t *testing.T) {
		version = "v0.2.0"
		readBuildInfo = func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}}, true
		}
		if got := reportedVersion(); got != "v0.2.0" {
			t.Fatalf("reportedVersion() = %q, want linker value", got)
		}
	})

	t.Run("installed module version", func(t *testing.T) {
		version = "0.0.0-dev"
		readBuildInfo = func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{Main: debug.Module{
				Version: "v0.1.0-alpha",
				Sum:     "h1:installed-module-checksum",
			}}, true
		}
		if got := reportedVersion(); got != "v0.1.0-alpha" {
			t.Fatalf("reportedVersion() = %q, want installed module version", got)
		}
	})

	t.Run("source checkout", func(t *testing.T) {
		version = "0.0.0-dev"
		readBuildInfo = func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{Main: debug.Module{
				Version: "v0.1.0-alpha.0.20260910022214-fdbf5c4307ad",
			}}, true
		}
		if got := reportedVersion(); got != "0.0.0-dev" {
			t.Fatalf("reportedVersion() = %q, want development version", got)
		}
	})
}

func TestRootCommand(t *testing.T) {
	observed := map[string]string{}

	t.Run("version", func(t *testing.T) {
		stdout, stderr, exit := run("--version")
		if exit != 0 {
			t.Fatalf("--version exit = %d, want 0", exit)
		}
		if !strings.Contains(stdout, version) {
			t.Fatalf("--version stdout = %q, want it to contain %q", stdout, version)
		}
		if stderr != "" {
			t.Fatalf("--version stderr = %q, want empty", stderr)
		}
		observed["version"] = "printed"
		observed["results"] = "stdout"
	})

	t.Run("no_args", func(t *testing.T) {
		stdout, stderr, exit := run()
		if exit != 0 {
			t.Fatalf("no-args exit = %d, want 0", exit)
		}
		if !strings.Contains(stdout, "Usage:") {
			t.Fatalf("no-args stdout = %q, want help output", stdout)
		}
		if stderr != "" {
			t.Fatalf("no-args stderr = %q, want empty", stderr)
		}
		observed["no_args"] = "help"
		observed["exit"] = "0"
	})

	t.Run("unknown_command", func(t *testing.T) {
		stdout, stderr, exit := run("bogus")
		if exit != ci.ExitInput {
			t.Fatalf("unknown command exit = %d, want %d", exit, ci.ExitInput)
		}
		if stdout != "" {
			t.Fatalf("unknown command stdout = %q, want empty (error only)", stdout)
		}
		if !strings.Contains(stderr, "bogus") {
			t.Fatalf("unknown command stderr = %q, want it to name the bad command", stderr)
		}
		if strings.Contains(stderr, "Usage:") {
			t.Fatalf("unknown command stderr = %q, want no usage dump", stderr)
		}
		observed["unknown_command"] = "5"
		observed["errors"] = "stderr"
		observed["usage_dump"] = "suppressed"
	})

	t.Run("unknown_flag", func(t *testing.T) {
		_, stderr, exit := run("--this-flag-does-not-exist")
		if exit != ci.ExitInput {
			t.Fatalf("unknown flag exit = %d, want %d", exit, ci.ExitInput)
		}
		if !strings.Contains(stderr, "this-flag-does-not-exist") {
			t.Fatalf("unknown flag stderr = %q, want it to name the bad flag", stderr)
		}
		observed["unknown_flag"] = "5"
	})

	order := []string{
		"version", "no_args", "exit", "unknown_command", "unknown_flag",
		"errors", "results", "usage_dump",
	}
	parts := make([]string, 0, len(order))
	for _, key := range order {
		value, ok := observed[key]
		if !ok {
			t.Fatalf("marker attribute %q was not observed by any subtest", key)
		}
		parts = append(parts, key+"="+value)
	}
	t.Logf("CLI_ROOT_RESULT %s", strings.Join(parts, " "))
}
