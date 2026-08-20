package diagnostic

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"
)

type Table struct {
	byCode    map[Code]Rule
	byTrigger map[Trigger]Rule
}

func Load(fsys fs.FS) (Table, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return Table{}, fmt.Errorf("load diagnostic rules: read table: %w", err)
	}
	table := Table{byCode: make(map[Code]Rule), byTrigger: make(map[Trigger]Rule)}
	for _, entry := range entries {
		if entry.IsDir() || path.Ext(entry.Name()) != ".json" {
			return Table{}, fmt.Errorf("load diagnostic rules: unexpected entry %q", entry.Name())
		}
		file, err := fsys.Open(entry.Name())
		if err != nil {
			return Table{}, fmt.Errorf("load diagnostic rule %q: %w", entry.Name(), err)
		}
		rule, decodeErr := decodeRule(file)
		closeErr := file.Close()
		if decodeErr != nil {
			return Table{}, fmt.Errorf("load diagnostic rule %q: %w", entry.Name(), decodeErr)
		}
		if closeErr != nil {
			return Table{}, fmt.Errorf("load diagnostic rule %q: close: %w", entry.Name(), closeErr)
		}
		filenameCode := Code(strings.TrimSuffix(entry.Name(), ".json"))
		if rule.Code != filenameCode {
			return Table{}, fmt.Errorf("load diagnostic rule %q: code %q does not match filename", entry.Name(), rule.Code)
		}
		if _, exists := table.byCode[rule.Code]; exists {
			return Table{}, fmt.Errorf("load diagnostic rule %q: duplicate code %q", entry.Name(), rule.Code)
		}
		for _, trigger := range rule.Triggers {
			if previous, exists := table.byTrigger[trigger]; exists {
				return Table{}, fmt.Errorf("load diagnostic rule %q: trigger %q already belongs to %s", entry.Name(), trigger, previous.Code)
			}
		}
		table.byCode[rule.Code] = rule
		for _, trigger := range rule.Triggers {
			table.byTrigger[trigger] = rule
		}
	}
	return table, nil
}

func decodeRule(reader io.Reader) (Rule, error) {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var rule Rule
	if err := decoder.Decode(&rule); err != nil {
		return Rule{}, err
	}
	if err := rule.Validate(); err != nil {
		return Rule{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Rule{}, fmt.Errorf("unexpected trailing JSON value")
		}
		return Rule{}, fmt.Errorf("read trailing data: %w", err)
	}
	return rule, nil
}

func (table Table) LookupTrigger(trigger Trigger) (Rule, bool) {
	rule, ok := table.byTrigger[trigger]
	return rule, ok
}

func (table Table) Codes() []Code {
	codes := make([]Code, 0, len(table.byCode))
	for code := range table.byCode {
		codes = append(codes, code)
	}
	slices.Sort(codes)
	return codes
}
