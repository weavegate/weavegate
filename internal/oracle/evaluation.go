package oracle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

// OracleResult records that one configured Oracle was evaluated. An empty
// Violations slice is an explicit PASS, not an omitted evaluation.
type OracleResult struct {
	OracleID   string      `json:"oracle_id"`
	Violations []Violation `json:"violations"`
}

// Evaluation is a declaration-ordered result set and its canonical fingerprint.
type Evaluation struct {
	Results     []OracleResult `json:"results"`
	Fingerprint string         `json:"fingerprint"`
}

// NewEvaluation validates and clones declaration-ordered results, normalizes
// empty collections, and computes their canonical fingerprint.
func NewEvaluation(results ...OracleResult) (Evaluation, error) {
	normalized, err := normalizeResults(results)
	if err != nil {
		return Evaluation{}, fmt.Errorf("create evaluation: %w", err)
	}
	fingerprint, err := fingerprintResults(normalized)
	if err != nil {
		return Evaluation{}, fmt.Errorf("create evaluation: fingerprint: %w", err)
	}
	return Evaluation{Results: normalized, Fingerprint: fingerprint}, nil
}

// ValidateEvaluation rejects malformed or tampered Evaluator output and returns
// an independently mutable normalized copy for storage by the caller.
func ValidateEvaluation(value Evaluation) (Evaluation, error) {
	if value.Fingerprint == "" {
		return Evaluation{}, errors.New("validate evaluation: fingerprint is required")
	}
	normalized, err := normalizeResults(value.Results)
	if err != nil {
		return Evaluation{}, fmt.Errorf("validate evaluation: %w", err)
	}
	fingerprint, err := fingerprintResults(normalized)
	if err != nil {
		return Evaluation{}, fmt.Errorf("validate evaluation: fingerprint: %w", err)
	}
	if value.Fingerprint != fingerprint {
		return Evaluation{}, fmt.Errorf(
			"validate evaluation: fingerprint mismatch: got %q, recomputed %q",
			value.Fingerprint,
			fingerprint,
		)
	}
	return Evaluation{Results: normalized, Fingerprint: fingerprint}, nil
}

func normalizeResults(results []OracleResult) ([]OracleResult, error) {
	if len(results) == 0 {
		return nil, errors.New("at least one oracle result is required")
	}

	normalized := make([]OracleResult, 0, len(results))
	seen := make(map[string]struct{}, len(results))
	for index, result := range results {
		cloned, err := normalizeOracleResult(result)
		if err != nil {
			return nil, fmt.Errorf("result[%d]: %w", index, err)
		}
		if _, exists := seen[cloned.OracleID]; exists {
			return nil, fmt.Errorf("result[%d]: duplicate oracle ID %q", index, cloned.OracleID)
		}
		seen[cloned.OracleID] = struct{}{}
		normalized = append(normalized, cloned)
	}
	return normalized, nil
}

func normalizeOracleResult(result OracleResult) (OracleResult, error) {
	if err := validateOracleID(result.OracleID); err != nil {
		return OracleResult{}, err
	}

	normalized := OracleResult{
		OracleID:   result.OracleID,
		Violations: make([]Violation, 0, len(result.Violations)),
	}
	for index, violation := range result.Violations {
		cloned, err := normalizeViolation(violation, result.OracleID)
		if err != nil {
			return OracleResult{}, fmt.Errorf("violation[%d]: %w", index, err)
		}
		normalized.Violations = append(normalized.Violations, cloned)
	}
	return normalized, nil
}

func normalizeViolation(value Violation, resultOracleID string) (Violation, error) {
	if value.OracleID != resultOracleID {
		return Violation{}, fmt.Errorf(
			"oracle ID %q does not match result oracle ID %q",
			value.OracleID,
			resultOracleID,
		)
	}
	switch value.Kind {
	case KindAssertion:
	default:
		return Violation{}, fmt.Errorf("unknown violation kind %q", value.Kind)
	}
	rows, err := cloneRows(value.Rows)
	if err != nil {
		return Violation{}, err
	}
	return Violation{OracleID: value.OracleID, Kind: value.Kind, Rows: rows}, nil
}

func cloneRows(rows []Row) ([]Row, error) {
	cloned := make([]Row, 0, len(rows))
	for index, row := range rows {
		if row == nil {
			return nil, fmt.Errorf("row[%d] is nil", index)
		}
		clonedRow := make(Row, len(row))
		for key, value := range row {
			if strings.TrimSpace(key) == "" {
				return nil, fmt.Errorf("row[%d] has a blank key", index)
			}
			if !utf8.ValidString(key) {
				return nil, fmt.Errorf("row[%d] key is not valid UTF-8", index)
			}
			if err := validateRowValue(value); err != nil {
				return nil, fmt.Errorf("row[%d] key %q: %w", index, key, err)
			}
			clonedRow[key] = value
		}
		cloned = append(cloned, clonedRow)
	}
	return cloned, nil
}

func validateRowValue(value any) error {
	switch typed := value.(type) {
	case nil, bool, int64, uint64:
		return nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return fmt.Errorf("non-finite float64 %v is not supported", typed)
		}
		return nil
	case string:
		if !utf8.ValidString(typed) {
			return errors.New("string is not valid UTF-8")
		}
		return nil
	default:
		return fmt.Errorf("value type %T is not supported", value)
	}
}

func fingerprintResults(results []OracleResult) (string, error) {
	canonical, err := normalizeResults(results)
	if err != nil {
		return "", err
	}
	for resultIndex := range canonical {
		result := &canonical[resultIndex]
		for violationIndex := range result.Violations {
			if err := sortCanonicalRows(result.Violations[violationIndex].Rows); err != nil {
				return "", fmt.Errorf(
					"encode result[%d] violation[%d] rows: %w",
					resultIndex,
					violationIndex,
					err,
				)
			}
		}
		if err := sortCanonicalViolations(result.Violations); err != nil {
			return "", fmt.Errorf("encode result[%d] violations: %w", resultIndex, err)
		}
	}
	sort.SliceStable(canonical, func(left, right int) bool {
		return canonical[left].OracleID < canonical[right].OracleID
	})

	payload := struct {
		Results []OracleResult `json:"results"`
	}{Results: canonical}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal canonical results: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func sortCanonicalRows(rows []Row) error {
	type keyedRow struct {
		value   Row
		encoded []byte
	}
	keyed := make([]keyedRow, 0, len(rows))
	for index, row := range rows {
		encoded, err := json.Marshal(row)
		if err != nil {
			return fmt.Errorf("row[%d]: %w", index, err)
		}
		keyed = append(keyed, keyedRow{value: row, encoded: encoded})
	}
	sort.SliceStable(keyed, func(left, right int) bool {
		return bytes.Compare(keyed[left].encoded, keyed[right].encoded) < 0
	})
	for index := range keyed {
		rows[index] = keyed[index].value
	}
	return nil
}

func sortCanonicalViolations(violations []Violation) error {
	type keyedViolation struct {
		value   Violation
		encoded []byte
	}
	keyed := make([]keyedViolation, 0, len(violations))
	for index, violation := range violations {
		encoded, err := json.Marshal(violation.Rows)
		if err != nil {
			return fmt.Errorf("violation[%d]: %w", index, err)
		}
		keyed = append(keyed, keyedViolation{value: violation, encoded: encoded})
	}
	sort.SliceStable(keyed, func(left, right int) bool {
		if keyed[left].value.Kind != keyed[right].value.Kind {
			return keyed[left].value.Kind < keyed[right].value.Kind
		}
		return bytes.Compare(keyed[left].encoded, keyed[right].encoded) < 0
	})
	for index := range keyed {
		violations[index] = keyed[index].value
	}
	return nil
}
