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

	violating := "none"
	if run.Scenario.ViolatingSchedule != nil {
		violating = run.Scenario.ViolatingSchedule.ID
	}
	fmt.Fprintf(
		&b,
		"scenario: %s | schedules explored: %s | violating: %s\n",
		run.Scenario.Name,
		explorationSummary(run),
		violating,
	)

	if len(run.Observation.AssertionViolations) > 0 {
		fmt.Fprintf(&b, "assertion: %s\n", strings.Join(run.Observation.AssertionViolations, ", "))
	}

	fmt.Fprintf(&b, "flaky: %t (repeat=%d)\n", run.Flaky, run.Observation.Repeat)

	if run.ReplayCommand != "" {
		fmt.Fprintf(&b, "replay: %s\n", run.ReplayCommand)
	}

	return b.String()
}

func explorationSummary(run Run) string {
	summary := strconv.Itoa(run.Observation.SchedulesExplored)
	if run.Pass && run.Scenario.ViolatingSchedule == nil {
		summary += " (exhausted)"
	}
	return summary
}
