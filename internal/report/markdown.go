package report

import (
	"fmt"
	"strconv"
	"strings"
)

const diagnosticLabelWidth = 11

// renderMarkdown builds report.md.
func renderMarkdown(run Run) string {
	var b strings.Builder

	headline := "FAIL"
	switch {
	case run.Flaky:
		headline = "FLAKY"
	case run.Pass:
		headline = "PASS"
	}
	if code := headlineDiagnosticCode(run); code != "" {
		headline += " (" + code + ")"
	}
	fmt.Fprintf(&b, "## weavegate: %s\n", headline)

	scheduleLabel := "violating"
	scheduleID := "none"
	if run.Scenario.Schedule != nil {
		scheduleID = run.Scenario.Schedule.ID
		if run.Observation.Mode == "replay" && run.Pass && !run.Flaky {
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
	for _, diagnostic := range run.Observation.Diagnostics {
		b.WriteByte('\n')
		renderDiagnostic(&b, diagnostic)
	}

	return b.String()
}

// headlineDiagnosticCode follows verdict priority rather than diagnostic list
// order. WG090 is rendered last in the body, but a flaky verdict must name the
// determinism failure that made the run untrustworthy.
func headlineDiagnosticCode(run Run) string {
	if run.Flaky {
		for _, diagnostic := range run.Observation.Diagnostics {
			if diagnostic.Code == "WG090" {
				return diagnostic.Code
			}
		}
		return ""
	}
	if len(run.Observation.Diagnostics) == 0 {
		return ""
	}
	return run.Observation.Diagnostics[0].Code
}

func renderDiagnostic(b *strings.Builder, diagnostic Diagnostic) {
	fmt.Fprintf(b, "%s[%s]: %s\n", diagnostic.Severity, diagnostic.Code, diagnostic.Title)
	writeDiagnosticField(b, "observed:", diagnostic.Observed)
	if diagnostic.Assertion != "" {
		writeDiagnosticField(b, "assertion:", diagnostic.Assertion)
	}
	writeDiagnosticField(b, "invariant:", diagnostic.Invariant)
	writeDiagnosticField(b, "reason:", diagnostic.Reason)
	for index, help := range diagnostic.Help {
		label := ""
		if index == 0 {
			label = "help:"
		}
		writeDiagnosticField(b, label, help)
	}
	parts := make([]string, 0, 3)
	if diagnostic.Evidence.ScheduleRef != "" {
		parts = append(parts, "schedule "+diagnostic.Evidence.ScheduleRef)
	}
	if diagnostic.Evidence.Trace != "" {
		parts = append(parts, diagnostic.Evidence.Trace)
	}
	if diagnostic.Evidence.Observation != "" {
		parts = append(parts, diagnostic.Evidence.Observation)
	}
	if diagnostic.Evidence.Rows > 0 {
		rowLabel := "violating rows"
		if diagnostic.Evidence.Rows == 1 {
			rowLabel = "violating row"
		}
		parts = append(parts, fmt.Sprintf("%d %s", diagnostic.Evidence.Rows, rowLabel))
	}
	if diagnostic.Evidence.EvidenceSets > 1 {
		parts = append(parts, fmt.Sprintf(
			"%d evidence sets in observation.json",
			diagnostic.Evidence.EvidenceSets,
		))
	}
	writeDiagnosticField(b, "evidence:", strings.Join(parts, " · "))
}

func writeDiagnosticField(b *strings.Builder, label, value string) {
	fmt.Fprintf(b, "  %-*s%s\n", diagnosticLabelWidth, label, value)
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
