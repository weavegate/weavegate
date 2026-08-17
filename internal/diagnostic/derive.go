package diagnostic

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/weavegate/weavegate/internal/oracle"
)

type Input struct {
	Table                Table
	Violations           []oracle.Violation
	OracleOrder          []string
	Flaky                bool
	Fingerprints         map[string]int
	DiscoveryFingerprint string
	ScheduleRef          string
}

type diagnosticGroup struct {
	rule       Rule
	oracleID   string
	firstRows  []oracle.Row
	totalRows  int
	orderIndex int
}

func Derive(input Input) ([]Diagnostic, error) {
	order := make(map[string]int, len(input.OracleOrder))
	for index, id := range input.OracleOrder {
		if _, exists := order[id]; exists {
			return nil, fmt.Errorf("derive diagnostics: duplicate oracle order ID %q", id)
		}
		order[id] = index
	}

	groups := make([]diagnosticGroup, 0, len(input.Violations))
	groupIndexes := make(map[string]int, len(input.Violations))
	for index, violation := range input.Violations {
		trigger, known := TriggerForKind(violation.Kind)
		if !known {
			return nil, fmt.Errorf("derive diagnostics: violation[%d]: unknown kind %q", index, violation.Kind)
		}
		rule, implemented := input.Table.LookupTrigger(trigger)
		if !implemented {
			continue
		}
		orderIndex, declared := order[violation.OracleID]
		if !declared {
			return nil, fmt.Errorf("derive diagnostics: violation[%d]: oracle %q is absent from declaration order", index, violation.OracleID)
		}
		key := string(rule.Code) + "\x00" + violation.OracleID
		if groupIndex, exists := groupIndexes[key]; exists {
			groups[groupIndex].totalRows += len(violation.Rows)
			continue
		}
		groupIndexes[key] = len(groups)
		groups = append(groups, diagnosticGroup{
			rule: rule, oracleID: violation.OracleID, firstRows: violation.Rows,
			totalRows: len(violation.Rows), orderIndex: orderIndex,
		})
	}
	sort.SliceStable(groups, func(left, right int) bool {
		return groups[left].orderIndex < groups[right].orderIndex
	})

	diagnostics := make([]Diagnostic, 0, len(groups)+1)
	for _, group := range groups {
		observed, err := renderObserved(group.oracleID, group.firstRows)
		if err != nil {
			return nil, fmt.Errorf("derive diagnostics for %q: %w", group.oracleID, err)
		}
		diagnostics = append(diagnostics, fromRule(group.rule, observed, group.oracleID, input.ScheduleRef, group.totalRows))
	}
	if input.Flaky {
		if rule, ok := input.Table.LookupTrigger(TriggerEngineDeterminism); ok {
			observed := renderFlakyObserved(input.Fingerprints, input.DiscoveryFingerprint)
			diagnostics = append(diagnostics, fromRule(
				rule,
				observed,
				"", input.ScheduleRef, 0,
			))
		}
	}
	return diagnostics, nil
}

func renderFlakyObserved(fingerprints map[string]int, discoveryFingerprint string) string {
	if len(fingerprints) > 1 {
		return fmt.Sprintf(
			"repeated executions produced %d normalized fingerprints",
			len(fingerprints),
		)
	}
	if len(fingerprints) == 1 && discoveryFingerprint != "" {
		for replayFingerprint := range fingerprints {
			if replayFingerprint != discoveryFingerprint {
				return "the discovery fingerprint differs from the replay fingerprint"
			}
		}
	}
	return "the determinism check reported divergent normalized results"
}

func fromRule(rule Rule, observed, assertion, scheduleRef string, rows int) Diagnostic {
	return Diagnostic{
		Code: rule.Code, Severity: rule.Severity, Title: rule.Title,
		Observed: observed, Assertion: assertion, Invariant: rule.Invariant,
		Reason: rule.Reason, Help: append([]string(nil), rule.Help...),
		Evidence: Evidence{ScheduleRef: scheduleRef, Rows: rows, Trace: "trace.json"},
	}
}

func renderObserved(oracleID string, rows []oracle.Row) (string, error) {
	rowLabel := "rows"
	if len(rows) == 1 {
		rowLabel = "row"
	}
	var rendered []string
	for index, row := range rows {
		keys := make([]string, 0, len(row))
		for key := range row {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		values := make([]string, 0, len(keys))
		for _, key := range keys {
			encoded, err := json.Marshal(row[key])
			if err != nil {
				return "", fmt.Errorf("row[%d] key %q: %w", index, key, err)
			}
			values = append(values, key+"="+string(encoded))
		}
		rendered = append(rendered, strings.Join(values, " "))
	}
	result := fmt.Sprintf("%s returned %d %s", oracleID, len(rows), rowLabel)
	if len(rendered) > 0 {
		result += ": " + strings.Join(rendered, "; ")
	}
	return result, nil
}
