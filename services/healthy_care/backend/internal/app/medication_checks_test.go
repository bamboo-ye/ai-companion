package app

import (
	"encoding/json"
	"testing"
)

func TestMedicationChecksDistinguishActualTimeUnknownAndNonuse(t *testing.T) {
	var records []map[string]any
	if err := json.Unmarshal([]byte(`[
 {"id":"a","data":{"name":"A","state":"used","ingredients":["甲"],"used_at":"2026-10-19T08:00:00Z","amount":"2"}},
 {"id":"b","data":{"name":"B","state":"used","ingredients":["甲"],"used_at":"2026-10-19T09:00:00Z"}},
 {"id":"c","data":{"name":"C","state":"not_used","ingredients":["甲"]}},
 {"id":"d","data":{"name":"D","state":"used","ingredients":["甲"]}}
 ]`), &records); err != nil {
		t.Fatal(err)
	}
	checks := medicationChecks(records)
	overlaps, units := 0, 0
	for _, c := range checks {
		switch c["rule"] {
		case "same_day_ingredient":
			overlaps++
			if len(c["record_ids"].([]string)) != 2 {
				t.Fatal("Unknown dates and explicit nonuse must not become overlaps")
			}
		case "unit_unknown":
			units++
		}
	}
	if overlaps != 1 || units != 1 {
		t.Fatalf("checks=%v", checks)
	}
}
