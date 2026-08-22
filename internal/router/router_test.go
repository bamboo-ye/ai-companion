package router

import (
	"encoding/json"
	"os"
	"sort"
	"testing"
)

type counts struct{ tp, fp, fn int }

func TestRouterExtractsRiskSkillAndSlots(t *testing.T) {
	tests := []struct {
		text, intent, skill, risk string
		required                  []string
	}{
		{text: "把这段话翻译成英文", intent: "office", skill: "office.translate", risk: "none"},
		{text: "给 client@example.com 写一封邮件", intent: "office", skill: "office.email_draft", risk: "none", required: []string{"subject"}},
		{text: "帮我写一封邮件，询问A学期可以选project课吗", intent: "office", skill: "office.email_draft", risk: "none", required: []string{"subject"}},
		{text: "帮我写一封英文邮件，询问课程安排", intent: "office", skill: "office.email_draft", risk: "none", required: []string{"subject"}},
		{text: "生成 8 页 PPT 给管理层", intent: "office", skill: "office.pptx_generate", risk: "none", required: []string{"style"}},
		{text: "明早提醒我开会", intent: "reminder", risk: "medium"},
		{text: "昨晚打车 36 元，帮我记账", intent: "ledger", risk: "medium"},
	}
	router := New()
	for _, test := range tests {
		result, err := router.Route(Input{Text: test.text})
		if err != nil || result.Intent != test.intent || result.SuggestedSkill != test.skill || result.RiskLevel != test.risk {
			t.Fatalf("route(%q) = %#v err=%v", test.text, result, err)
		}
		for _, slot := range test.required {
			if !contains(result.RequiredSlots, slot) {
				t.Fatalf("route(%q) required slots = %v, missing %s", test.text, result.RequiredSlots, slot)
			}
		}
	}
	englishEmail, err := router.Route(Input{Text: "帮我写一封英文邮件，询问课程安排"})
	if err != nil || englishEmail.Slots["output_language"] != "en-US" {
		t.Fatalf("English email language slot = %#v err=%v", englishEmail.Slots, err)
	}
}

func TestIntentFixtureMacroF1(t *testing.T) {
	data, err := os.ReadFile("../../evals/m4_intent_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Text   string `json:"text"`
		Intent string `json:"intent"`
	}
	if err = json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	labels := map[string]bool{}
	metrics := map[string]*counts{}
	router := New()
	for _, item := range cases {
		result, routeErr := router.Route(Input{Text: item.Text})
		if routeErr != nil {
			t.Fatal(routeErr)
		}
		labels[item.Intent], labels[result.Intent] = true, true
		if result.Intent == item.Intent {
			entry := ensure(metrics, item.Intent)
			entry.tp++
		} else {
			ensure(metrics, result.Intent).fp++
			ensure(metrics, item.Intent).fn++
		}
	}
	ordered := make([]string, 0, len(labels))
	for label := range labels {
		ordered = append(ordered, label)
	}
	sort.Strings(ordered)
	macro := 0.0
	for _, label := range ordered {
		entry := ensure(metrics, label)
		denominator := 2*entry.tp + entry.fp + entry.fn
		if denominator > 0 {
			macro += float64(2*entry.tp) / float64(denominator)
		}
	}
	macro /= float64(len(ordered))
	if macro < 0.95 {
		t.Fatalf("macro-F1 = %.4f, want >= 0.95; metrics=%#v", macro, metrics)
	}
}

func TestRouterRejectsEmptyAndOversizedInput(t *testing.T) {
	if _, err := New().Route(Input{}); err == nil {
		t.Fatal("empty input must fail")
	}
	value := make([]rune, 4001)
	for index := range value {
		value[index] = '长'
	}
	if _, err := New().Route(Input{Text: string(value)}); err == nil {
		t.Fatal("oversized input must fail")
	}
}

func ensure(values map[string]*counts, key string) *counts {
	if values[key] == nil {
		values[key] = &counts{}
	}
	return values[key]
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
