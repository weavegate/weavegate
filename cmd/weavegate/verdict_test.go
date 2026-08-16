package main

import (
	"strings"
	"testing"

	"github.com/weavegate/weavegate/internal/ci"
	"github.com/weavegate/weavegate/internal/oracle"
	"github.com/weavegate/weavegate/internal/orchestrator"
)

func violatingEvaluation(t *testing.T) oracle.Evaluation {
	t.Helper()
	evaluation, err := oracle.NewEvaluation(oracle.OracleResult{
		OracleID:   "active-assignment-is-unique",
		Violations: []oracle.Violation{{OracleID: "active-assignment-is-unique", Kind: oracle.KindAssertion, Rows: []oracle.Row{{"n": int64(1)}}}},
	})
	if err != nil {
		t.Fatalf("build violating evaluation: %v", err)
	}
	return evaluation
}

func passingEvaluation(t *testing.T) oracle.Evaluation {
	t.Helper()
	evaluation, err := oracle.NewEvaluation(oracle.OracleResult{
		OracleID:   "active-assignment-is-unique",
		Violations: []oracle.Violation{},
	})
	if err != nil {
		t.Fatalf("build passing evaluation: %v", err)
	}
	return evaluation
}

func repeatRuns(evaluation oracle.Evaluation, fingerprint string, n int) []orchestrator.RunResult {
	runs := make([]orchestrator.RunResult, 0, n)
	for i := 0; i < n; i++ {
		runs = append(runs, orchestrator.RunResult{Evaluation: evaluation, Fingerprint: fingerprint})
	}
	return runs
}

func TestComputeVerdict(t *testing.T) {
	observed := map[string]string{}
	violating := violatingEvaluation(t)
	passing := passingEvaluation(t)

	t.Run("all_violation", func(t *testing.T) {
		got := ComputeVerdict(VerdictInput{
			Mode:                 ModeExplore,
			DiscoveryFingerprint: "fp-violation",
			Replay: orchestrator.ReplayResult{
				Flaky:        false,
				Fingerprints: map[string]int{"fp-violation": 20},
				Runs:         repeatRuns(violating, "fp-violation", 20),
			},
		})
		if got.Flaky || verdictExitCode(got) != ci.ExitViolation || got.ViolationRuns != 20 {
			t.Fatalf("all-violation verdict = %+v, want mapped exit=2 flaky=false violation_runs=20", got)
		}
		observed["all_violation"] = "2"
	})

	t.Run("partial_violation", func(t *testing.T) {
		got := ComputeVerdict(VerdictInput{
			Mode:                 ModeExplore,
			DiscoveryFingerprint: "fp-violation",
			Replay: orchestrator.ReplayResult{
				Flaky: true, // mismatched fingerprints across runs, as orchestrator.Replay sets it
				Fingerprints: map[string]int{
					"fp-violation": 19,
					"fp-pass":      1,
				},
				Runs: append(repeatRuns(violating, "fp-violation", 19), repeatRuns(passing, "fp-pass", 1)...),
			},
		})
		if !got.Flaky || verdictExitCode(got) != ci.ExitFlaky {
			t.Fatalf("partial-violation verdict = %+v, want exit=3 flaky=true", got)
		}
		observed["partial_violation"] = "3"
	})

	t.Run("all_pass_after_discovery", func(t *testing.T) {
		got := ComputeVerdict(VerdictInput{
			Mode:                 ModeExplore,
			DiscoveryFingerprint: "fp-violation",
			Replay: orchestrator.ReplayResult{
				Flaky:        false, // every replay run agrees with every other
				Fingerprints: map[string]int{"fp-pass": 20},
				Runs:         repeatRuns(passing, "fp-pass", 20),
			},
		})
		if !got.Flaky || verdictExitCode(got) != ci.ExitFlaky {
			t.Fatalf("0/20 after discovery verdict = %+v, want exit=3 flaky=true (must not leak to PASS)", got)
		}
		observed["all_pass_after_discovery"] = "3"
	})

	t.Run("fingerprint_mismatch", func(t *testing.T) {
		got := ComputeVerdict(VerdictInput{
			Mode:                 ModeExplore,
			DiscoveryFingerprint: "fp-violation-A",
			Replay: orchestrator.ReplayResult{
				Flaky:        false,
				Fingerprints: map[string]int{"fp-violation-B": 20},
				Runs:         repeatRuns(violating, "fp-violation-B", 20),
			},
		})
		if !got.Flaky || verdictExitCode(got) != ci.ExitFlaky {
			t.Fatalf("fingerprint-mismatch verdict = %+v, want exit=3 flaky=true", got)
		}
		observed["fingerprint_mismatch"] = "3"
	})

	t.Run("replay_mode_flaky", func(t *testing.T) {
		got := ComputeVerdict(VerdictInput{
			Mode: ModeReplay,
			Replay: orchestrator.ReplayResult{
				Flaky:        true,
				Fingerprints: map[string]int{"fp-a": 10, "fp-b": 10},
				Runs:         append(repeatRuns(violating, "fp-a", 10), repeatRuns(passing, "fp-b", 10)...),
			},
		})
		if !got.Flaky || verdictExitCode(got) != ci.ExitFlaky {
			t.Fatalf("replay-mode flaky verdict = %+v, want exit=3 flaky=true", got)
		}
		observed["replay_mode_flaky"] = "3"
	})

	t.Run("exhausted", func(t *testing.T) {
		got := ComputeVerdict(VerdictInput{Mode: ModeExplore, Exhausted: true})
		if got.Flaky || verdictExitCode(got) != ci.ExitOK {
			t.Fatalf("exhausted verdict = %+v, want exit=0 flaky=false", got)
		}
		observed["exhausted"] = "0"
	})

	order := []string{
		"all_violation", "partial_violation", "all_pass_after_discovery",
		"fingerprint_mismatch", "replay_mode_flaky", "exhausted",
	}
	parts := make([]string, 0, len(order))
	for _, key := range order {
		value, ok := observed[key]
		if !ok {
			t.Fatalf("marker attribute %q was not observed by any subtest", key)
		}
		parts = append(parts, key+"="+value)
	}
	t.Logf("CLI_VERDICT_RESULT %s", strings.Join(parts, " "))
}

func verdictExitCode(verdict Verdict) int {
	return ci.ExitCode(nil, ci.Verdict{Violations: verdict.ViolationRuns, Flaky: verdict.Flaky})
}
