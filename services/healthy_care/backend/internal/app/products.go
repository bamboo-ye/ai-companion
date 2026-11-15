package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

type ProductLeaflet struct {
	ID              string   `json:"id"`
	Version         string   `json:"version"`
	Name            string   `json:"name"`
	Region          string   `json:"region"`
	Strength        string   `json:"strength"`
	Form            string   `json:"form"`
	Route           string   `json:"route"`
	Ingredients     []string `json:"ingredients"`
	Information     string   `json:"information"`
	DosageLabel     string   `json:"dosage_label"`
	Warnings        []string `json:"warnings"`
	AmountUnit      string   `json:"amount_unit"`
	ReviewAfterDays *int     `json:"review_after_days,omitempty"`
	MinAge          int      `json:"min_age"`
	MaxAge          int      `json:"max_age"`
	SourceURL       string   `json:"source_url"`
	ReviewStatus    string   `json:"review_status"`
	Reviewer        string   `json:"reviewer"`
	ReviewedAt      string   `json:"reviewed_at"`
	ReviewReference string   `json:"review_reference"`
	ValidUntil      string   `json:"valid_until"`
	DataOrigin      string   `json:"data_origin"`
}

var productID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,100}$`)

func validateLeaflet(p ProductLeaflet, now time.Time, allowSynthetic bool) error {
	if !productID.MatchString(p.ID) || !productID.MatchString(p.Version) || p.Name == "" || p.Strength == "" || p.Form == "" || p.Route == "" || len(p.Region) != 2 || p.Region != strings.ToUpper(p.Region) || len(p.Ingredients) == 0 || len(p.Ingredients) > 20 || p.Information == "" {
		return errors.New("product identity, region, specification and ingredients are required")
	}
	if p.DataOrigin != "reference" && p.DataOrigin != "synthetic" {
		return errors.New("invalid product data origin")
	}
	if p.DataOrigin == "synthetic" && !allowSynthetic {
		return errors.New("synthetic leaflets are allowed only in the explicit test environment")
	}
	for _, s := range append(append([]string{p.Name, p.Strength, p.Form, p.Route, p.Information, p.DosageLabel, p.Reviewer, p.ReviewReference}, p.Ingredients...), p.Warnings...) {
		if len(s) > 8000 {
			return errors.New("product field too long")
		}
	}
	u, err := url.Parse(p.SourceURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return errors.New("leaflet source must be an HTTPS URL")
	}
	until, err := time.Parse(time.RFC3339, p.ValidUntil)
	if err != nil || !until.After(now) {
		return errors.New("leaflet validity must be explicit and in the future")
	}
	if p.MinAge < 18 || p.MaxAge < p.MinAge || p.MaxAge > 120 {
		return errors.New("adult age bounds must be explicit")
	}
	if p.ReviewAfterDays != nil && (*p.ReviewAfterDays < 1 || *p.ReviewAfterDays > 365) {
		return errors.New("invalid product-specific review node")
	}
	if p.AmountUnit != "" && unitDimension(p.AmountUnit) == "" {
		return errors.New("unsupported leaflet quantity unit")
	}
	if p.ReviewStatus != "reference" && p.ReviewStatus != "reviewed" {
		return errors.New("use the retire command to withdraw an existing leaflet")
	}
	if p.ReviewStatus == "reviewed" {
		at, err := time.Parse(time.RFC3339, p.ReviewedAt)
		if err != nil || at.After(now) || p.Reviewer == "" || p.ReviewReference == "" {
			return errors.New("reviewed content requires a reviewer, review evidence and review date")
		}
	}
	return nil
}

// Administrative, stdin-only import. No HTTP publication endpoint or implicit
// professional approval is exposed to end users or the model.
func (a *App) ImportProducts(ctx context.Context, input io.Reader) error {
	var bundle struct {
		SchemaVersion string           `json:"schema_version"`
		Products      []ProductLeaflet `json:"products"`
	}
	d := json.NewDecoder(io.LimitReader(input, 2<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(&bundle); err != nil {
		return err
	}
	if bundle.SchemaVersion != "1" || len(bundle.Products) == 0 || len(bundle.Products) > 100 {
		return errors.New("invalid product bundle")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("one JSON bundle is required")
	}
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, p := range bundle.Products {
		if err = validateLeaflet(p, time.Now(), a.Config.AllowSyntheticKnowledge); err != nil {
			return fmt.Errorf("product %s: %w", p.ID, err)
		}
		var old string
		err = tx.QueryRow(ctx, "SELECT content_sha256 FROM product_leaflets WHERE id=$1 AND version=$2", p.ID, p.Version).Scan(&old)
		sum := digest(p)
		if err == nil {
			if old != sum {
				return errors.New("an existing leaflet version is immutable; publish a new version")
			}
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO product_leaflets(id,version,data,content_sha256,review_status,valid_until) VALUES($1,$2,$3,$4,$5,$6)", p.ID, p.Version, encode(p), sum, p.ReviewStatus, p.ValidUntil); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO audit_events(action,resource_id,purpose) VALUES('import_leaflet',$1,'knowledge')", p.ID+":"+p.Version); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func (a *App) RetireProduct(ctx context.Context, id, version string) error {
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, "UPDATE product_leaflets SET review_status='retired' WHERE id=$1 AND version=$2", id, version)
	if err == nil && tag.RowsAffected() == 0 {
		return errors.New("product version not found")
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(action,resource_id,purpose) VALUES('retire_leaflet',$1,'knowledge')", id+":"+version); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func loadLeaflets(ctx context.Context, q querier) ([]ProductLeaflet, error) {
	values, err := rows(ctx, q, "SELECT data,review_status FROM product_leaflets ORDER BY id,version")
	if err != nil {
		return nil, err
	}
	out := []ProductLeaflet{}
	for _, v := range values {
		var p ProductLeaflet
		if err = json.Unmarshal(encode(v["data"]), &p); err != nil {
			return nil, err
		}
		p.ReviewStatus = fmt.Sprint(v["review_status"])
		out = append(out, p)
	}
	return out, nil
}
func published(p ProductLeaflet, now time.Time, allowSynthetic bool) bool {
	until, err := time.Parse(time.RFC3339, p.ValidUntil)
	return err == nil && p.ReviewStatus == "reviewed" && until.After(now) && (p.DataOrigin != "synthetic" || allowSynthetic)
}
func leafletMetadata(p ProductLeaflet) map[string]any {
	return map[string]any{"id": p.ID, "version": p.Version, "name": p.Name, "region": p.Region, "strength": p.Strength, "form": p.Form, "route": p.Route, "ingredients": p.Ingredients, "information": p.Information, "warnings": p.Warnings, "source_url": p.SourceURL, "review_status": p.ReviewStatus, "valid_until": p.ValidUntil, "data_origin": p.DataOrigin}
}
func (a *App) products(c *gin.Context) {
	ps, err := loadLeaflets(c, a.DB)
	if err != nil {
		internalError(c, err)
		return
	}
	out := []map[string]any{}
	for _, p := range ps {
		if published(p, time.Now(), a.Config.AllowSyntheticKnowledge) && (c.Query("region") == "" || p.Region == c.Query("region")) && (c.Query("q") == "" || strings.Contains(strings.ToLower(p.Name), strings.ToLower(c.Query("q")))) {
			out = append(out, leafletMetadata(p))
		}
	}
	c.JSON(200, gin.H{"items": out})
}
func validateProductBinding(ctx context.Context, q querier, data map[string]any) error {
	id, version := text(data, "product_id"), text(data, "leaflet_version")
	if id == "" && version == "" {
		return nil
	}
	ps, err := loadLeaflets(ctx, q)
	if err != nil {
		return err
	}
	for _, p := range ps {
		if p.ID == id && p.Version == version {
			for key, value := range map[string]string{"name": p.Name, "region": p.Region, "strength": p.Strength, "form": p.Form, "route": p.Route} {
				if !strings.EqualFold(text(data, key), value) {
					return invalid("产品名称、地区、规格、剂型或途径尚未完全对应，先补充信息或解除说明关联。")
				}
			}
			return nil
		}
	}
	return invalid("没有找到指定的产品说明版本；实际用药仍可不关联说明单独记录。")
}
func dosageEligibility(p ProductLeaflet, profile map[string]any) []string {
	missing := []string{}
	age, ok := number(profile, "age")
	if !ok || age < float64(p.MinAge) || age > float64(p.MaxAge) {
		missing = append(missing, "适用年龄")
	}
	if text(profile, "region") != p.Region {
		missing = append(missing, "使用地区")
	}
	pregnancy := text(profile, "pregnancy_status")
	if pregnancy != "neither_pregnant_nor_breastfeeding" && pregnancy != "not_applicable" {
		missing = append(missing, "孕哺情况")
	}
	for _, field := range []struct{ Key, Label string }{{"allergies", "过敏背景"}, {"conditions", "相关病史"}} {
		values, ok := profile[field.Key].([]any)
		if !ok || len(values) > 0 {
			missing = append(missing, field.Label)
		}
	}
	medications, known := profile["current_medications"].([]any)
	if !known {
		missing = append(missing, "在用药背景")
	} else {
		for _, value := range medications {
			name, ok := value.(string)
			if !ok || (!strings.EqualFold(strings.TrimSpace(name), p.Name) && name != p.ID) {
				missing = append(missing, "其他在用药背景")
				break
			}
		}
	}
	return missing
}
func productInformation(ps []ProductLeaflet, records []map[string]any, profile map[string]any, now time.Time, allowSynthetic bool) []map[string]any {
	out := []map[string]any{}
	for _, record := range records {
		data, _ := record["data"].(map[string]any)
		for _, p := range ps {
			if text(data, "product_id") != p.ID || text(data, "leaflet_version") != p.Version || !published(p, now, allowSynthetic) {
				continue
			}
			// Bindings are rechecked on every read, including after a product amendment.
			matches := true
			for k, v := range map[string]string{"name": p.Name, "region": p.Region, "strength": p.Strength, "form": p.Form, "route": p.Route} {
				if !strings.EqualFold(text(data, k), v) {
					matches = false
				}
			}
			if !matches {
				continue
			}
			info := leafletMetadata(p)
			info["record_id"] = record["id"]
			missing := dosageEligibility(p, profile)
			info["missing_background"] = missing
			info["dosage_available"] = len(missing) == 0 && p.DosageLabel != ""
			if len(missing) == 0 {
				info["dosage_label"] = p.DosageLabel
			}
			out = append(out, info)
		}
	}
	return out
}
