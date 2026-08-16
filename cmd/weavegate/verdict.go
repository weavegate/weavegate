package main

import (
	"github.com/weavegate/weavegate/internal/oracle"
	"github.com/weavegate/weavegate/internal/orchestrator"
)

// Mode names which flow produced the replay result a verdict is computed
// from.
type Mode string

const (
	ModeExplore Mode = "explore"
	ModeReplay  Mode = "replay"
)

// VerdictInput is the evidence a verdict is computed from. Exhausted is
// meaningful only in explore mode: it is true when every pass swept its full
// candidate set without finding a violation, in which case Replay was never
// called. DiscoveryFingerprint is the fingerprint of the run exploration
// first found violating; it is compared against the replay's fingerprint to
// catch a violation that explore found once but replay cannot reproduce.
type VerdictInput struct {
	Mode                 Mode
	Exhausted            bool
	DiscoveryFingerprint string
	Replay               orchestrator.ReplayResult
}

// Verdict is the decided outcome of a run.
type Verdict struct {
	Flaky         bool
	ViolationRuns int
}

// ComputeVerdict decides a run's verdict without touching a database. Flaky
// takes priority over violation (A-17): a discovery that cannot be
// reproduced on replay — including the 0/repeat case, where every replay run
// passes — is a determinism failure, not a clean PASS.
func ComputeVerdict(input VerdictInput) Verdict {
	if input.Mode == ModeExplore && input.Exhausted {
		return Verdict{}
	}

	flaky := input.Replay.Flaky
	if input.Mode == ModeExplore {
		if uniqueFingerprint(input.Replay.Fingerprints) != input.DiscoveryFingerprint {
			flaky = true
		}
	}

	violationRuns := 0
	for _, run := range input.Replay.Runs {
		if evaluationViolationCount(run.Evaluation) > 0 {
			violationRuns++
		}
	}

	return Verdict{Flaky: flaky, ViolationRuns: violationRuns}
}

// uniqueFingerprint returns the sole fingerprint key when exactly one
// distinct fingerprint was observed, or "" otherwise (zero or ambiguous).
func uniqueFingerprint(fingerprints map[string]int) string {
	if len(fingerprints) != 1 {
		return ""
	}
	for fingerprint := range fingerprints {
		return fingerprint
	}
	return ""
}

func evaluationViolationCount(evaluation oracle.Evaluation) int {
	violations := 0
	for _, result := range evaluation.Results {
		violations += len(result.Violations)
	}
	return violations
}
