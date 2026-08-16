package ci

import (
	"errors"
	"strings"
	"testing"
)

func TestExitCode(t *testing.T) {
	observed := map[string]string{}
	plain := errors.New("boom")

	t.Run("pass", func(t *testing.T) {
		if got := ExitCode(nil, Verdict{}); got != ExitOK {
			t.Fatalf("ExitCode(nil, pass) = %d, want %d", got, ExitOK)
		}
		observed["pass"] = "0"
	})

	t.Run("violation", func(t *testing.T) {
		if got := ExitCode(nil, Verdict{Violations: 1}); got != ExitViolation {
			t.Fatalf("ExitCode(nil, violation) = %d, want %d", got, ExitViolation)
		}
		observed["violation"] = "2"
	})

	t.Run("flaky", func(t *testing.T) {
		if got := ExitCode(nil, Verdict{Flaky: true}); got != ExitFlaky {
			t.Fatalf("ExitCode(nil, flaky) = %d, want %d", got, ExitFlaky)
		}
		observed["flaky"] = "3"
	})

	t.Run("fixture", func(t *testing.T) {
		if got := ExitCode(FixtureError(plain), Verdict{}); got != ExitFixture {
			t.Fatalf("ExitCode(FixtureError, _) = %d, want %d", got, ExitFixture)
		}
		observed["fixture"] = "4"
	})

	t.Run("input", func(t *testing.T) {
		if got := ExitCode(InputError(plain), Verdict{}); got != ExitInput {
			t.Fatalf("ExitCode(InputError, _) = %d, want %d", got, ExitInput)
		}
		observed["input"] = "5"
	})

	t.Run("output", func(t *testing.T) {
		if got := ExitCode(OutputError(plain), Verdict{}); got != ExitInput {
			t.Fatalf("ExitCode(OutputError, _) = %d, want %d", got, ExitInput)
		}
		observed["output"] = "5"
	})

	t.Run("interrupted", func(t *testing.T) {
		if got := ExitCode(InterruptedError(plain), Verdict{}); got != ExitInterrupted {
			t.Fatalf("ExitCode(InterruptedError, _) = %d, want %d", got, ExitInterrupted)
		}
		observed["interrupted"] = "130"
	})

	t.Run("unclassified", func(t *testing.T) {
		if got := ExitCode(plain, Verdict{}); got != ExitInput {
			t.Fatalf("ExitCode(plain error, _) = %d, want %d (unclassified must not silently pass)", got, ExitInput)
		}
		observed["unclassified"] = "5"
	})

	t.Run("error_beats_verdict", func(t *testing.T) {
		got := ExitCode(FixtureError(plain), Verdict{Violations: 5, Flaky: true})
		if got != ExitFixture {
			t.Fatalf("ExitCode(FixtureError, violating+flaky verdict) = %d, want %d", got, ExitFixture)
		}
		observed["error_beats_verdict"] = "true"
	})

	t.Run("flaky_beats_violation", func(t *testing.T) {
		got := ExitCode(nil, Verdict{Flaky: true, Violations: 5})
		if got != ExitFlaky {
			t.Fatalf("ExitCode(nil, flaky+violating) = %d, want %d", got, ExitFlaky)
		}
		observed["flaky_beats_violation"] = "true"
	})

	t.Run("cleanup_on_pass", func(t *testing.T) {
		got := ExitCode(nil, Verdict{CleanupFailed: true})
		if got != ExitFixture {
			t.Fatalf("ExitCode(nil, pass+cleanup_failed) = %d, want %d", got, ExitFixture)
		}
		observed["cleanup_on_pass"] = "4"
	})

	t.Run("cleanup_keeps_violation", func(t *testing.T) {
		got := ExitCode(nil, Verdict{Violations: 1, CleanupFailed: true})
		if got != ExitViolation {
			t.Fatalf("ExitCode(nil, violation+cleanup_failed) = %d, want %d", got, ExitViolation)
		}
		observed["cleanup_keeps_violation"] = "2"
	})

	t.Run("cleanup_never_masks", func(t *testing.T) {
		got := ExitCode(nil, Verdict{Flaky: true, CleanupFailed: true})
		if got != ExitFlaky {
			t.Fatalf("ExitCode(nil, flaky+cleanup_failed) = %d, want %d", got, ExitFlaky)
		}
		observed["cleanup_never_masks"] = "true"
	})

	order := []string{
		"pass", "violation", "flaky", "fixture", "input", "output", "interrupted", "unclassified",
		"error_beats_verdict", "flaky_beats_violation",
		"cleanup_on_pass", "cleanup_keeps_violation", "cleanup_never_masks",
	}
	parts := make([]string, 0, len(order))
	for _, key := range order {
		value, ok := observed[key]
		if !ok {
			t.Fatalf("marker attribute %q was not observed by any subtest", key)
		}
		parts = append(parts, key+"="+value)
	}
	t.Logf("EXIT_CODE_RESULT %s", strings.Join(parts, " "))
}
