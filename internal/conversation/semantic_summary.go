package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/windcry1/ai-companion/internal/semantic"
	"sort"
	"strings"
)

// Summary facts carry original message IDs across rolls. The assistant's claims
// remain labelled as such; they are never silently promoted to user facts.
type SummaryFact struct {
	Kind    string   `json:"kind"`
	Text    string   `json:"text"`
	Sources []string `json:"sources"`
	Role    string   `json:"role"`
}
type StructuredSummary struct {
	Facts   []SummaryFact `json:"facts"`
	Omitted int           `json:"omitted,omitempty"`
}

func (s *Service) SetSemanticClient(client *semantic.Client) { s.contextBuilder.semantic = client }
func (b *ContextBuilder) summarize(ctx context.Context, previous *ConversationSummary, messages []Message) (string, string) {
	data := StructuredSummary{Facts: []SummaryFact{}}
	if previous != nil {
		if json.Unmarshal([]byte(previous.Content), &data) != nil {
			data.Facts = []SummaryFact{{Kind: "background", Text: previous.Content, Sources: []string{"summary:" + previous.ID}, Role: "mixed"}}
		}
	}
	allowed := map[string]bool{}
	roles := map[string]string{}
	for _, f := range data.Facts {
		for _, source := range f.Sources {
			allowed[source] = true
			roles[source] = f.Role
		}
	}
	for _, m := range messages {
		source := m.ID
		if source == "" {
			source = fmt.Sprintf("sequence:%d:%d", m.Sequence, m.Bubble)
		}
		allowed[source] = true
		roles[source] = m.Role
		// Split into complete sentences so late constraints in long messages survive.
		for _, sentence := range strings.FieldsFunc(m.Content, func(r rune) bool { return r == '\n' || r == '。' || r == '！' || r == '？' }) {
			sentence = strings.TrimSpace(sentence)
			if sentence == "" {
				continue
			}
			kind := "background"
			lower := strings.ToLower(sentence)
			switch {
			case containsAny(lower, "必须", "不要", "不能", "禁止", "约束", "预算", "must", "never", "constraint"):
				kind = "constraint"
			case containsAny(lower, "目标", "想要", "希望", "goal", "objective"):
				kind = "goal"
			case containsAny(lower, "待办", "接下来", "尚未", "pending", "todo"):
				kind = "pending"
			case containsAny(lower, "已完成", "已经", "完成了", "completed", "done"):
				kind = "done"
			case m.Role == "user":
				kind = "fact"
			}
			data.Facts = append(data.Facts, SummaryFact{Kind: kind, Text: sentence, Sources: []string{source}, Role: m.Role})
		}
	}
	version := "structured-extractive-v2"
	if b.semantic.SummariesEnabled() {
		var candidate StructuredSummary
		err := b.semantic.JSON(ctx, `Compress the supplied conversation facts into {"facts":[{"kind":"goal|constraint|fact|done|pending|background","text":"concise fact","sources":["original ID"],"role":"user|assistant|tool|mixed"}]}. Preserve goals, early constraints, unresolved work and corrections. Every fact must cite supplied original IDs. Distinguish user statements from assistant claims. Do not invent facts.`, data, b.summaryBudget, &candidate)
		if err == nil && validSummary(candidate, allowed, roles) {
			// Protected original constraints survive even if the model omits one.
			for _, fact := range data.Facts {
				if fact.Kind == "constraint" && fact.Role == "user" {
					candidate.Facts = append(candidate.Facts, fact)
				}
			}
			candidate.Omitted = data.Omitted
			data = candidate
			version = "structured-semantic-v2"
		}
	}
	sort.SliceStable(data.Facts, func(i, j int) bool { return summaryPriority(data.Facts[i]) < summaryPriority(data.Facts[j]) })
	result := StructuredSummary{Facts: []SummaryFact{}, Omitted: data.Omitted}
	seen := map[string]bool{}
	for _, f := range data.Facts {
		key := f.Role + ":" + f.Text
		if seen[key] {
			continue
		}
		seen[key] = true
		result.Facts = append(result.Facts, f)
		encoded, _ := json.Marshal(result)
		if EstimateTokens(string(encoded))+12 > b.summaryBudget {
			result.Facts = result.Facts[:len(result.Facts)-1]
			result.Omitted++
		}
	}
	encoded, _ := json.Marshal(result)
	return string(encoded), version
}
func summaryPriority(f SummaryFact) int {
	if f.Kind == "constraint" && f.Role == "user" {
		return -1
	}
	switch f.Kind {
	case "constraint":
		return 0
	case "goal":
		return 1
	case "pending":
		return 2
	case "fact":
		return 3
	case "done":
		return 4
	}
	return 5
}
func containsAny(s string, words ...string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}
func validSummary(s StructuredSummary, allowed map[string]bool, roles map[string]string) bool {
	if len(s.Facts) == 0 || len(s.Facts) > 100 {
		return false
	}
	for _, f := range s.Facts {
		if strings.TrimSpace(f.Text) == "" || len(f.Text) > 4000 || len(f.Sources) == 0 || !containsExact([]string{"goal", "constraint", "fact", "done", "pending", "background"}, f.Kind) || !containsExact([]string{"user", "assistant", "tool", "mixed"}, f.Role) {
			return false
		}
		for _, id := range f.Sources {
			if !allowed[id] || (f.Role == "user" && roles[id] != "user") {
				return false
			}
		}
	}
	return true
}
func containsExact(values []string, s string) bool {
	for _, v := range values {
		if v == s {
			return true
		}
	}
	return false
}
