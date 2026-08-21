package main

import (
	"reflect"
	"testing"
)

func TestCompareKeys(t *testing.T) {
	missing, extra := compareKeys(
		[]string{"a", "b", "c"},
		[]string{"b", "c", "d"},
	)
	if !reflect.DeepEqual(missing, []string{"a"}) || !reflect.DeepEqual(extra, []string{"d"}) {
		t.Fatalf("compareKeys() = %#v, %#v", missing, extra)
	}
}

func TestCoreTableDependencyOrder(t *testing.T) {
	specs := coreTables()
	if len(specs) != 13 {
		t.Fatalf("coreTables() length = %d", len(specs))
	}
	if specs[0].name != "users" || specs[len(specs)-1].name != "outbox_events" {
		t.Fatalf("unexpected dependency order: first=%q last=%q", specs[0].name, specs[len(specs)-1].name)
	}
}

func TestLifeTableDependencyOrder(t *testing.T) {
	specs := lifeTables()
	if len(specs) != 10 {
		t.Fatalf("lifeTables() length = %d", len(specs))
	}
	if specs[0].name != "ledger_candidates" || specs[len(specs)-1].name != "workspace_ledger_export_shares" {
		t.Fatalf("unexpected dependency order: first=%q last=%q", specs[0].name, specs[len(specs)-1].name)
	}
	resets := sequenceResetStatements(specs)
	if len(resets) != 1 {
		t.Fatalf("sequenceResetStatements(life) length = %d", len(resets))
	}
}

func TestToolTableDependencyOrder(t *testing.T) {
	specs := toolTables()
	if len(specs) != 14 {
		t.Fatalf("toolTables() length = %d", len(specs))
	}
	if specs[0].name != "files" || specs[len(specs)-1].name != "user_skill_settings" {
		t.Fatalf("unexpected dependency order: first=%q last=%q", specs[0].name, specs[len(specs)-1].name)
	}
	resets := sequenceResetStatements(specs)
	if len(resets) != 1 {
		t.Fatalf("sequenceResetStatements(tools) length = %d", len(resets))
	}
}

func TestGovernanceTableDependencyOrder(t *testing.T) {
	specs := governanceTables()
	if len(specs) != 12 {
		t.Fatalf("governanceTables() length = %d", len(specs))
	}
	if specs[0].name != "billing_plans" || specs[len(specs)-1].name != "compensation_records" {
		t.Fatalf("unexpected dependency order: first=%q last=%q", specs[0].name, specs[len(specs)-1].name)
	}
	if resets := sequenceResetStatements(specs); len(resets) != 2 {
		t.Fatalf("sequenceResetStatements(governance) length = %d", len(resets))
	}
	if finalizers := finalizationStatements(specs); len(finalizers) != 3 {
		t.Fatalf("finalizationStatements(governance) length = %d", len(finalizers))
	}
}

func TestNormalizeBooleans(t *testing.T) {
	values := []any{int64(0), int64(1), "false", "true"}
	if err := normalizeBooleans(0, 1, 2, 3)(values); err != nil {
		t.Fatalf("normalizeBooleans() error = %v", err)
	}
	if !reflect.DeepEqual(values, []any{false, true, false, true}) {
		t.Fatalf("normalizeBooleans() = %#v", values)
	}
}
