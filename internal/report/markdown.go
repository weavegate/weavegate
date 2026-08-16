package report

import (
	"fmt"
	"strconv"
	"strings"
)

// renderMarkdown builds report.md. RG diagnostic codes are not part of the
// headline yet (A-10) — that follows once M7 exists.
func renderMarkdown(run Run) string {
	var b strings.Builder

	headline := "FAIL"
	switch {
	case run.Flaky:
		headline = "FLAKY"
	case run.Pass:
		headline = "PASS"
	}
	fmt.Fprintf(&b, "## weavegate: %s\n", headline)

	scheduleLabel := "violating"
	scheduleID := "none"
	if run.Scenario.Schedule != nil {
		scheduleID = run.Scenario.Schedule.ID
		if run.Observation.Mode == "replay" && run.Observation.ViolationRuns == 0 {
			scheduleLabel = "replayed"
		}
	}
	fmt.Fprintf(
		&b,
		"scenario: %s | schedules explored: %s | %s: %s\n",
		run.Scenario.Name,
		explorationSummary(run),
		scheduleLabel,
		scheduleID,
	)

	if ids := violatedAssertionIDs(run.Observation.AssertionViolations); len(ids) > 0 {
		fmt.Fprintf(&b, "assertion: %s\n", strings.Join(ids, ", "))
	}

	fmt.Fprintf(&b, "flaky: %t (repeat=%d)\n", run.Flaky, run.Observation.Repeat)

	if run.ReplayCommand != "" {
		fmt.Fprintf(&b, "replay: %s\n", run.ReplayCommand)
	}

	return b.String()
}

func explorationSummary(run Run) string {
	summary := strconv.Itoa(run.Observation.SchedulesExplored)
	if run.Pass && run.Scenario.Schedule == nil {
		summary += " (exhausted)"
	}
	return summary
}

// violatedAssertionIDs collects the distinct oracle IDs behind violations,
// in first-seen order. A single ID can carry more than one AssertionViolation
// entry (distinct evidence rows from different runs), but the headline lists
// each violated assertion once.
func violatedAssertionIDs(violations []AssertionViolation) []string {
	seen := make(map[string]struct{}, len(violations))
	ids := make([]string, 0, len(violations))
	for _, violation := range violations {
		if _, exists := seen[violation.OracleID]; exists {
			continue
		}
		seen[violation.OracleID] = struct{}{}
		ids = append(ids, violation.OracleID)
	}
	return ids
}
