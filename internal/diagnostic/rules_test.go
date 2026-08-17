package diagnostic

import (
	"fmt"
	"os"
	"testing"
	"testing/fstest"

	shippedrules "github.com/weavegate/weavegate/rules"
)

func TestEmbeddedRuleTableContract(t *testing.T) {
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Chdir(outside); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })

	table, err := Load(shippedrules.FS())
	if err != nil {
		t.Fatalf("load embedded rules outside checkout: %v", err)
	}
	if got := fmt.Sprint(table.Codes()); got != "[RG001 RG090]" {
		t.Fatalf("codes = %s", got)
	}
	if rule, ok := table.LookupTrigger(TriggerOracleAssertion); !ok || rule.Code != "RG001" {
		t.Fatalf("assertion lookup = %#v, %t", rule, ok)
	}
	fmt.Println("DIAGNOSTIC_RULES_RESULT codes=2 source=embedded filename_code_match=enforced duplicate_code=error duplicate_trigger=error unknown_trigger=error missing_help=error lookup=by_trigger order=sorted outside_repo=true")
}

func TestLoadRejectsInvalidTables(t *testing.T) {
	valid := `{"code":"RG001","severity":"error","triggers":["oracle.assertion"],"title":"t","invariant":"i","reason":"r","help":["h"]}`
	tests := map[string]fstest.MapFS{
		"filename mismatch": {"RG090.json": {Data: []byte(valid)}},
		"duplicate code": {
			"RG001.json": {Data: []byte(valid)},
			"copy.json":  {Data: []byte(valid)},
		},
		"duplicate trigger": {
			"RG001.json": {Data: []byte(valid)},
			"RG090.json": {Data: []byte(`{"code":"RG090","severity":"error","triggers":["oracle.assertion"],"title":"t","invariant":"i","reason":"r","help":["h"]}`)},
		},
		"unknown trigger": {"RG001.json": {Data: []byte(`{"code":"RG001","severity":"error","triggers":["oracle.typo"],"title":"t","invariant":"i","reason":"r","help":["h"]}`)}},
		"missing help":    {"RG001.json": {Data: []byte(`{"code":"RG001","severity":"error","triggers":["oracle.assertion"],"title":"t","invariant":"i","reason":"r","help":[]}`)}},
		"non json":        {"README.md": {Data: []byte("no")}},
	}
	for name, filesystem := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(filesystem); err == nil {
				t.Fatal("Load succeeded")
			}
		})
	}
}
