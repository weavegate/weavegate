package sqlassert

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/weavegate/weavegate/internal/oracle"
)

func readRows(rows *sql.Rows) ([]oracle.Row, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("read columns: %w", err)
	}
	columnTypes, err := rows.ColumnTypes()
	if err != nil {
		return nil, fmt.Errorf("read column types: %w", err)
	}
	if len(columnTypes) != len(columns) {
		return nil, fmt.Errorf(
			"column metadata count = %d, want %d",
			len(columnTypes),
			len(columns),
		)
	}
	seen := make(map[string]struct{}, len(columns))
	for index, column := range columns {
		if strings.TrimSpace(column) == "" {
			return nil, fmt.Errorf("column[%d] has a blank name", index)
		}
		if !utf8.ValidString(column) {
			return nil, fmt.Errorf("column[%d] name is not valid UTF-8", index)
		}
		if _, exists := seen[column]; exists {
			return nil, fmt.Errorf("column[%d] has duplicate name %q", index, column)
		}
		seen[column] = struct{}{}
	}

	evidence := make([]oracle.Row, 0)
	for rows.Next() {
		values := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return nil, fmt.Errorf("scan row[%d]: %w", len(evidence), err)
		}

		row := make(oracle.Row, len(columns))
		for index, value := range values {
			normalized, err := normalizeValue(value, columnTypes[index].DatabaseTypeName())
			if err != nil {
				return nil, fmt.Errorf(
					"normalize row[%d] column %q: %w",
					len(evidence),
					columns[index],
					err,
				)
			}
			row[columns[index]] = normalized
		}
		evidence = append(evidence, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rows: %w", err)
	}
	if err := sortEvidence(evidence); err != nil {
		return nil, fmt.Errorf("sort evidence: %w", err)
	}
	return evidence, nil
}

func normalizeValue(value any, databaseType string) (any, error) {
	switch typed := value.(type) {
	case nil, bool, int64, uint64:
		return typed, nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return nil, fmt.Errorf("non-finite float64 %v is not supported", typed)
		}
		return typed, nil
	case string:
		if !utf8.ValidString(typed) {
			return nil, errors.New("string is not valid UTF-8")
		}
		return typed, nil
	case time.Time:
		return typed.UTC().Format(time.RFC3339Nano), nil
	case []byte:
		if isIntegerDatabaseType(databaseType) {
			return normalizeIntegerBytes(typed, databaseType)
		}
		if !utf8.Valid(typed) {
			return nil, errors.New("bytes are not valid UTF-8")
		}
		return string(typed), nil
	default:
		return nil, fmt.Errorf("driver value type %T is not supported", value)
	}
}

func normalizeIntegerBytes(value []byte, databaseType string) (any, error) {
	text := string(value)
	if isUnsignedDatabaseType(databaseType) {
		number, err := strconv.ParseUint(text, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse %s value %q as uint64: %w", databaseType, text, err)
		}
		return number, nil
	}
	number, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse %s value %q as int64: %w", databaseType, text, err)
	}
	return number, nil
}

func isIntegerDatabaseType(databaseType string) bool {
	for _, field := range strings.Fields(strings.ToUpper(databaseType)) {
		field = strings.Trim(field, "(),")
		if width := strings.IndexByte(field, '('); width >= 0 {
			field = field[:width]
		}
		switch field {
		case "TINYINT", "SMALLINT", "MEDIUMINT", "INT", "INTEGER", "BIGINT":
			return true
		}
	}
	return false
}

func isUnsignedDatabaseType(databaseType string) bool {
	for _, field := range strings.Fields(strings.ToUpper(databaseType)) {
		if strings.Trim(field, "(),") == "UNSIGNED" {
			return true
		}
	}
	return false
}

func sortEvidence(rows []oracle.Row) error {
	type keyedRow struct {
		value   oracle.Row
		encoded []byte
	}
	keyed := make([]keyedRow, 0, len(rows))
	for index, row := range rows {
		encoded, err := json.Marshal(row)
		if err != nil {
			return fmt.Errorf("marshal row[%d]: %w", index, err)
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
