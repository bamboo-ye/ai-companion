package conversation

import (
	"context"
	"encoding/json"
	"github.com/windcry1/ai-companion/internal/semantic"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStructuredSummaryRetainsEarlyAndLateConstraintsWithSources(t *testing.T) {
	b := NewContextBuilder(NewMemoryStore(), 128, 500)
	messages := []Message{{ID: "goal", Role: "user", Sequence: 1, Content: "目标：完成项目。必须使用中文"}, {ID: "noise", Role: "assistant", Sequence: 2, Content: strings.Repeat("普通背景。", 100) + "不要发送邮件"}, {ID: "later", Role: "user", Sequence: 3, Content: "接下来运行测试"}}
	text, version := b.summarize(context.Background(), nil, messages)
	var summary StructuredSummary
	if err := json.Unmarshal([]byte(text), &summary); err != nil {
		t.Fatal(err)
	}
	if version != "structured-extractive-v2" || !strings.Contains(text, "必须使用中文") || !strings.Contains(text, "不要发送邮件") || !strings.Contains(text, "goal") {
		t.Fatalf("lost constraint %s", text)
	}
	rolled, _ := b.summarize(context.Background(), &ConversationSummary{Content: text}, []Message{{ID: "more", Role: "assistant", Content: strings.Repeat("完成情况。", 100)}})
	if !strings.Contains(rolled, "必须使用中文") || EstimateTokens(rolled) > 500 {
		t.Fatal("rolling lost early constraint or budget")
	}
}
func TestSemanticSummaryRejectsInventedSource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		content := `{"facts":[{"kind":"fact","text":"invented","sources":["not-a-source"],"role":"user"}]}`
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}}}})
	}))
	defer server.Close()
	b := NewContextBuilder(NewMemoryStore(), 128, 400)
	b.semantic = semantic.New(semantic.Config{BaseURL: server.URL, SummaryModel: "test"})
	text, version := b.summarize(context.Background(), nil, []Message{{ID: "real", Role: "user", Content: "必须保留约束"}})
	if version != "structured-extractive-v2" || strings.Contains(text, "invented") {
		t.Fatal("ungrounded summary accepted")
	}
}

func TestSemanticSummaryCannotPromoteAssistantClaimsToUserFacts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		content := `{"facts":[{"kind":"done","text":"已经付款","sources":["assistant-message"],"role":"user"}]}`
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}}}})
	}))
	defer server.Close()
	b := NewContextBuilder(NewMemoryStore(), 128, 400)
	b.semantic = semantic.New(semantic.Config{BaseURL: server.URL, SummaryModel: "test"})
	text, version := b.summarize(context.Background(), nil, []Message{{ID: "assistant-message", Role: "assistant", Content: "已经付款"}})
	if version != "structured-extractive-v2" || !strings.Contains(text, `"role":"assistant"`) {
		t.Fatalf("assistant claim promoted: %s %s", version, text)
	}
}
