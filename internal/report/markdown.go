package report

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
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
	writeMarkdownLine(&b, "## weavegate: %s", headline)

	scheduleLabel := "violating"
	scheduleID := "none"
	if run.Scenario.Schedule != nil {
		scheduleID = run.Scenario.Schedule.ID
		if run.Observation.Mode == "replay" && run.Pass && !run.Flaky {
			scheduleLabel = "replayed"
		}
	}
	writeMarkdownLine(
		&b,
		"scenario: %s | schedules explored: %s | %s: %s",
		run.Scenario.Name,
		explorationSummary(run),
		scheduleLabel,
		scheduleID,
	)

	if ids := violatedAssertionIDs(run.Observation.AssertionViolations); len(ids) > 0 {
		writeMarkdownLine(&b, "assertion: %s", strings.Join(ids, ", "))
	}

	writeMarkdownLine(&b, "flaky: %t (repeat=%d)", run.Flaky, run.Observation.Repeat)

	if run.ReplayCommand != "" {
		writeMarkdownLine(&b, "replay: %s", run.ReplayCommand)
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
	writeMarkdownLine(b, "%s[%s]: %s", diagnostic.Severity, diagnostic.Code, diagnostic.Title)
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
	writeMarkdownLine(b, "  %-*s%s", diagnosticLabelWidth, label, value)
}

// writeMarkdownLine is the only boundary where variable text enters
// report.md. Callers supply the fixed Markdown shape as format and each
// runtime value separately; the boundary makes every value safe before it is
// interpolated and terminates the physical line itself.
func writeMarkdownLine(b *strings.Builder, format string, values ...any) {
	safe := make([]any, len(values))
	for index, value := range values {
		if text, ok := value.(string); ok {
			safe[index] = escapeMarkdownValue(text)
			continue
		}
		safe[index] = value
	}
	fmt.Fprintf(b, format+"\n", safe...)
}

// escapeMarkdownValue preserves ordinary text while preventing a runtime
// value from creating another physical line, emitting terminal controls, or
// opening Markdown inline/block syntax. Non-printable runes use the same Go
// escape spelling as strconv.Quote; Markdown punctuation is backslash-escaped.
func escapeMarkdownValue(value string) string {
	runes, invalidBytes := decodeMarkdownRunes(value)
	var escaped strings.Builder
	for index, r := range runes {
		if invalidBytes[index] != 0 {
			fmt.Fprintf(&escaped, `\x%02x`, invalidBytes[index])
			continue
		}
		if !unicode.IsPrint(r) {
			quoted := strconv.QuoteRune(r)
			escaped.WriteString(quoted[1 : len(quoted)-1])
			continue
		}
		if r == '$' {
			escaped.WriteString(`\x24`)
			continue
		}
		if index == 0 && r == ' ' && len(runes) >= 4 && runes[1] == ' ' && runes[2] == ' ' && runes[3] == ' ' {
			escaped.WriteString(`\x20`)
			continue
		}
		if markdownDelimiter(r) || r == '_' && !intraWordUnderscore(runes, index) ||
			markdownListDelimiter(runes, index) ||
			markdownAutolinkDelimiter(runes, index) {
			escaped.WriteByte('\\')
		}
		escaped.WriteRune(r)
	}
	return escaped.String()
}

// decodeMarkdownRunes retains malformed UTF-8 bytes separately from genuine
// U+FFFD runes. A range loop would replace every malformed byte with U+FFFD and
// make distinct filesystem paths render identically.
func decodeMarkdownRunes(value string) ([]rune, []byte) {
	runes := make([]rune, 0, utf8.RuneCountInString(value))
	invalidBytes := make([]byte, 0, cap(runes))
	for len(value) > 0 {
		r, size := utf8.DecodeRuneInString(value)
		runes = append(runes, r)
		invalidByte := byte(0)
		if r == utf8.RuneError && size == 1 {
			invalidByte = value[0]
		}
		invalidBytes = append(invalidBytes, invalidByte)
		value = value[size:]
	}
	return runes, invalidBytes
}

func markdownDelimiter(r rune) bool {
	switch r {
	case '\\', '`', '*', '[', ']', '<', '>', '&', '~', '@', '#', '|':
		return true
	default:
		return false
	}
}

func intraWordUnderscore(runes []rune, index int) bool {
	return index > 0 && index+1 < len(runes) &&
		wordRune(runes[index-1]) && wordRune(runes[index+1])
}

func wordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

func markdownListDelimiter(runes []rune, index int) bool {
	if index+1 >= len(runes) || !unicode.IsSpace(runes[index+1]) {
		return false
	}
	start := 0
	for start < index && start < 4 && runes[start] == ' ' {
		start++
	}
	if start == index {
		return runes[index] == '-' || runes[index] == '+'
	}
	if start > 3 || index-start == 0 || index-start > 9 ||
		(runes[index] != '.' && runes[index] != ')') {
		return false
	}
	for _, r := range runes[start:index] {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func markdownAutolinkDelimiter(runes []rune, index int) bool {
	if runes[index] == ':' && index > 0 && index+2 < len(runes) &&
		runes[index+1] == '/' && runes[index+2] == '/' {
		start := index - 1
		for start > 0 && schemeRune(runes[start-1]) {
			start--
		}
		return unicode.IsLetter(runes[start])
	}
	return runes[index] == '.' && index >= 3 &&
		strings.EqualFold(string(runes[index-3:index]), "www") &&
		(index == 3 || !wordRune(runes[index-4]))
}

func schemeRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '+' || r == '-' || r == '.'
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
