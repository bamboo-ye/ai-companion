package app

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

var recordedAmount = regexp.MustCompile(`^\s*([0-9]+(?:\.[0-9]+)?)\s*([a-zA-Z\p{Han}]+)\s*$`)

func unitDimension(unit string) string {
	return map[string]string{"片": "tablet", "tablet": "tablet", "粒": "capsule", "capsule": "capsule", "ml": "volume", "毫升": "volume", "mg": "mass", "毫克": "mass", "g": "mass", "克": "mass"}[strings.ToLower(strings.TrimSpace(unit))]
}
func leafletChecks(ps []ProductLeaflet, records []map[string]any, now time.Time, allowSynthetic bool) []map[string]any {
	out := []map[string]any{}
	for _, p := range ps {
		byEpisode := map[string][]map[string]any{}
		for _, record := range records {
			data, _ := record["data"].(map[string]any)
			if text(data, "product_id") != p.ID || text(data, "leaflet_version") != p.Version || text(data, "state") != "used" {
				continue
			}
			source := map[string]any{"title": p.Name, "source_url": p.SourceURL, "version": p.Version}
			add := func(rule, title, message string, facts []string, ids []any) {
				out = append(out, map[string]any{"id": rule + "-" + fmt.Sprint(record["id"]), "rule": rule, "title": title, "message": message, "facts": facts, "record_ids": ids, "source": source, "rule_version": "leaflet-context-1"})
			}
			if !published(p, now, allowSynthetic) {
				add("leaflet_unavailable", "关联说明需要重新核对", "这份说明未发布、已过期或已撤回；记录保留，当前不提供该版本的用量信息。", []string{p.Name, p.Version}, []any{record["id"]})
				continue
			}
			if parts := recordedAmount.FindStringSubmatch(text(data, "amount")); len(parts) > 0 && unitDimension(p.AmountUnit) != "" && unitDimension(parts[2]) != "" && unitDimension(parts[2]) != unitDimension(p.AmountUnit) {
				add("leaflet_unit_mismatch", "记录单位与说明不对应", "请核对产品规格、原始单位和实际记录，当前不作换算或过量结论。", []string{text(data, "amount"), "说明单位：" + p.AmountUnit}, []any{record["id"]})
			}
			if at, err := time.Parse(time.RFC3339, text(data, "used_at")); err == nil && !at.After(now) {
				key := fmt.Sprint(record["episode_id"])
				byEpisode[key] = append(byEpisode[key], map[string]any{"id": record["id"], "at": at})
			}
		}
		if p.ReviewAfterDays != nil && published(p, now, allowSynthetic) {
			for _, uses := range byEpisode {
				if len(uses) < 2 {
					continue
				}
				first, last := uses[0], uses[0]
				for _, u := range uses {
					if u["at"].(time.Time).Before(first["at"].(time.Time)) {
						first = u
					}
					if u["at"].(time.Time).After(last["at"].(time.Time)) {
						last = u
					}
				}
				span := int(last["at"].(time.Time).Sub(first["at"].(time.Time)).Hours() / 24)
				if span >= *p.ReviewAfterDays {
					out = append(out, map[string]any{"id": "review-node-" + fmt.Sprint(first["id"]), "rule": "leaflet_review_node", "title": "实际使用记录跨过说明的复核节点", "facts": []string{p.Name, fmt.Sprintf("两次记录相隔%d天，说明复核节点%d天", span, *p.ReviewAfterDays)}, "message": "记录跨度不证明连续使用，也不自动决定停药；请核对目前是否仍在使用、使用依据与该说明的适用条件。", "record_ids": []any{first["id"], last["id"]}, "source": map[string]any{"source_url": p.SourceURL, "version": p.Version}, "rule_version": "leaflet-context-1"})
				}
			}
		}
	}
	return out
}
