package app

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

var amountUnit = regexp.MustCompile(`(?i)[0-9]+(?:\.[0-9]+)?\s*(mg|g|ml|毫克|克|毫升|片|粒|滴|喷|次|包|袋|贴)`)

// Checks describe confirmed record contents, never infer an individual prescription.
func medicationChecks(records []map[string]any) []map[string]any {
	out := []map[string]any{}
	grouped := map[string]map[string]map[string]bool{}
	for _, record := range records {
		data, _ := record["data"].(map[string]any)
		if text(data, "state") != "used" {
			continue
		}
		id := fmt.Sprint(record["id"])
		missing := []string{}
		ingredients, _ := data["ingredients"].([]any)
		if len(ingredients) == 0 {
			missing = append(missing, "已确认成分")
		}
		for _, field := range []struct{ Key, Label string }{{"strength", "规格"}, {"route", "使用途径"}, {"amount", "实际用量"}, {"frequency", "频次"}, {"used_at", "实际使用时间"}, {"basis", "使用依据"}} {
			if text(data, field.Key) == "" {
				missing = append(missing, field.Label)
			}
		}
		if len(missing) > 0 {
			out = append(out, map[string]any{"id": "missing-" + id, "rule": "information_gap", "title": text(data, "name") + "：信息待补充", "facts": missing, "record_ids": []string{id}, "message": "可以补充药盒说明与实际使用情况。缺少信息不能视为已经误用。", "rule_version": "personal-medication-1"})
		}
		if value := text(data, "amount"); value != "" && !amountUnit.MatchString(value) {
			out = append(out, map[string]any{"id": "unit-" + id, "rule": "unit_unknown", "title": "请核对实际用量的单位", "facts": []string{text(data, "name"), value}, "record_ids": []string{id}, "message": "保留了原始文字，尚不能换算或与产品说明比较。", "rule_version": "personal-medication-1"})
		}
		usedAt, err := time.Parse(time.RFC3339, text(data, "used_at"))
		if err != nil {
			continue
		}
		day := usedAt.UTC().Format("2006-01-02")
		if grouped[day] == nil {
			grouped[day] = map[string]map[string]bool{}
		}
		for _, item := range ingredients {
			ing, ok := item.(string)
			if !ok {
				continue
			}
			ing = strings.ToLower(strings.TrimSpace(ing))
			if ing == "" {
				continue
			}
			if grouped[day][ing] == nil {
				grouped[day][ing] = map[string]bool{}
			}
			grouped[day][ing][id] = true
		}
	}
	days := []string{}
	for day := range grouped {
		days = append(days, day)
	}
	sort.Strings(days)
	for _, day := range days {
		keys := []string{}
		for ing := range grouped[day] {
			keys = append(keys, ing)
		}
		sort.Strings(keys)
		for _, ing := range keys {
			records := grouped[day][ing]
			if len(records) < 2 {
				continue
			}
			ids := []string{}
			for id := range records {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			out = append(out, map[string]any{"id": "duplicate-" + day + "-" + ing, "rule": "same_day_ingredient", "title": "同日记录出现相同成分", "facts": []string{day + "（UTC）", ing}, "record_ids": ids, "message": "请核对这些记录是否属于同一产品的多次记录，还是不同产品；同日出现本身不能证明过量或同时使用。", "rule_version": "personal-medication-1"})
		}
	}
	return out
}

func dailyInsights(s map[string]any) []map[string]any {
	out := []map[string]any{}
	observations := s["observations"].([]map[string]any)
	for _, e := range s["episodes"].([]map[string]any) {
		var first, last map[string]any
		count := 0
		for _, o := range observations {
			if o["episode_id"] != e["id"] {
				continue
			}
			data, _ := o["data"].(map[string]any)
			if _, ok := number(data, "score"); !ok {
				continue
			}
			count++
			if first == nil {
				first = o
			}
			last = o
		}
		if first == nil {
			continue
		}
		f, _ := first["data"].(map[string]any)
		l, _ := last["data"].(map[string]any)
		title := "最近自评分"
		message := fmt.Sprintf("%v分，来自1个实际记录日；还不足以描述变化。", l["score"])
		if count > 1 {
			title = "实际记录的变化"
			message = fmt.Sprintf("%v → %v分，来自%d个实际记录日；未记录日期未补值，变化不等于疾病结论。", f["score"], l["score"], count)
		}
		out = append(out, map[string]any{"episode_id": e["id"], "complaint": e["complaint"], "status": e["status"], "title": title, "message": message, "record_ids": []any{first["id"], last["id"]}})
		if len(out) == 3 {
			break
		}
	}
	return out
}
