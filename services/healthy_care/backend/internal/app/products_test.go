package app

import (
	"encoding/json"
	"testing"
	"time"
)

func syntheticLeaflet() ProductLeaflet {
	return ProductLeaflet{ID: "synthetic-product", Version: "v1", Name: "合成产品", Region: "ZZ", Strength: "合成规格", Form: "测试剂型", Route: "测试途径", Ingredients: []string{"合成成分"}, Information: "合成说明，仅用于接口验收。", DosageLabel: "合成用量字段，不可用于实际用药。", MinAge: 18, MaxAge: 64, SourceURL: "https://example.test/synthetic", ReviewStatus: "reviewed", Reviewer: "synthetic-fixture", ReviewedAt: "2026-01-01T00:00:00Z", ReviewReference: "synthetic-review-fixture", ValidUntil: "2099-01-01T00:00:00Z", DataOrigin: "synthetic"}
}
func TestProductPublicationEvidenceAndSyntheticBoundary(t *testing.T) {
	p := syntheticLeaflet()
	now := time.Now()
	if validateLeaflet(p, now, false) == nil {
		t.Fatal("Production must reject synthetic leaflet publication")
	}
	if err := validateLeaflet(p, now, true); err != nil {
		t.Fatal(err)
	}
	p.Reviewer = ""
	if validateLeaflet(p, now, true) == nil {
		t.Fatal("Reviewed status must not bypass evidence requirements")
	}
	p = syntheticLeaflet()
	p.ValidUntil = "2000-01-01T00:00:00Z"
	if published(p, now, true) {
		t.Fatal("Expired leaflet published")
	}
	p = syntheticLeaflet()
	p.ReviewStatus = "retired"
	if published(p, now, true) {
		t.Fatal("Retired leaflet published")
	}
}
func TestProductDoseInformationRequiresIdentityAndBackground(t *testing.T) {
	p := syntheticLeaflet()
	var profile map[string]any
	json.Unmarshal([]byte(`{"age":28,"region":"ZZ","pregnancy_status":"not_applicable","allergies":[],"conditions":[],"current_medications":[]}`), &profile)
	records := []map[string]any{{"id": "record", "data": map[string]any{"product_id": p.ID, "leaflet_version": p.Version, "name": p.Name, "region": p.Region, "strength": p.Strength, "form": p.Form, "route": p.Route}}}
	info := productInformation([]ProductLeaflet{p}, records, map[string]any{}, time.Now(), true)
	if _, exposed := info[0]["dosage_label"]; exposed {
		t.Fatal("Missing background exposed dose label")
	}
	info = productInformation([]ProductLeaflet{p}, records, profile, time.Now(), true)
	if info[0]["dosage_label"] != p.DosageLabel {
		t.Fatal("Matched and reviewed source was not available")
	}
	profile["pregnancy_status"] = "not_pregnant"
	if len(dosageEligibility(p, profile)) == 0 {
		t.Fatal("Not pregnant alone does not establish breastfeeding status")
	}
	profile["pregnancy_status"] = "not_applicable"
	profile["current_medications"] = []any{p.Name}
	if len(dosageEligibility(p, profile)) != 0 {
		t.Fatal("The matched product itself is not an unknown additional medicine")
	}
	profile["current_medications"] = []any{"another product"}
	if len(dosageEligibility(p, profile)) == 0 {
		t.Fatal("Other medicines require an additional applicability review")
	}
	profile["current_medications"] = []any{}

	records[0]["data"].(map[string]any)["strength"] = "other"
	if len(productInformation([]ProductLeaflet{p}, records, profile, time.Now(), true)) != 0 {
		t.Fatal("Different strength must not match")
	}
	if _, exposed := leafletMetadata(p)["dosage_label"]; exposed {
		t.Fatal("Catalog lookup must not reveal dose label")
	}
}
func TestLeafletUnitsAndProductSpecificReviewNode(t *testing.T) {
	p := syntheticLeaflet()
	p.AmountUnit = "片"
	n := 4
	p.ReviewAfterDays = &n
	records := []map[string]any{}
	for _, date := range []string{"2026-10-01T00:00:00Z", "2026-10-08T00:00:00Z"} {
		records = append(records, map[string]any{"id": date, "episode_id": "same-episode", "data": map[string]any{"state": "used", "product_id": p.ID, "leaflet_version": p.Version, "amount": "2ml", "used_at": date}})
	}
	checks := leafletChecks([]ProductLeaflet{p}, records, time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC), true)
	found := map[string]int{}
	for _, c := range checks {
		found[c["rule"].(string)]++
	}
	if found["leaflet_unit_mismatch"] != 2 || found["leaflet_review_node"] != 1 {
		t.Fatalf("%v", found)
	}
}
