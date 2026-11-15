package app

import (
	"testing"
	"time"
)

func TestHistoryNegationAndFutureBoundaries(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	obs := func(id, code, assertion string, days int) map[string]any {
		return map[string]any{"id": id, "observed_date": now.AddDate(0, 0, days), "data": map[string]any{"symptoms": []any{map[string]any{"code": code, "assertion": assertion}}}}
	}
	base := []map[string]any{obs("1", "thirst", "present", -2), obs("2", "urination", "present", -1), obs("3", "weight_loss", "present", 0)}
	if len(matchHistory(base, now)) != 1 {
		t.Fatal("explicit historical combination not found")
	}
	if len(matchHistory(append(base, obs("4", "thirst", "absent", 0)), now)) != 0 {
		t.Fatal("negation must supersede previous positive report")
	}
	if len(matchHistory([]map[string]any{base[0], base[1], obs("future", "weight_loss", "present", 1)}, now)) != 0 {
		t.Fatal("future data must not leak into historical match")
	}
	if len(matchHistory([]map[string]any{obs("old", "thirst", "present", -31), base[1], base[2]}, now)) != 0 {
		t.Fatal("expired evidence must not create current signal")
	}
}
func TestPayloadRejectsAmbiguousTypes(t *testing.T) {
	for _, p := range []map[string]any{{"granted": "true"}, {"age": 12.5}, {"ingredients": map[string]any{"name": "unknown"}}, {"symptoms": []any{map[string]any{"code": "thirst", "assertion": "diagnosed"}}}} {
		if validatePayload("test", p) == nil {
			t.Fatalf("invalid action accepted: %#v", p)
		}
	}
}
