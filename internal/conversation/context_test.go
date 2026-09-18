package conversation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestContextBuilderKeepsCompleteRecentGroupsAndRollsSummary(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	started := time.Date(2026, 7, 3, 1, 0, 0, 0, time.UTC)
	store.conversations["c1"] = Conversation{ID: "c1", UserID: "u1"}
	store.messages["c1"] = []Message{
		{ID: "m1", ConversationID: "c1", UserID: "u1", Role: "user", Sequence: 1, Bubble: 1, Content: strings.Repeat("早期用户信息", 8), CreatedAt: started},
		{ID: "m2a", ConversationID: "c1", UserID: "u1", Role: "assistant", Sequence: 2, Bubble: 1, Content: strings.Repeat("早期回复甲", 6), CreatedAt: started.Add(time.Minute)},
		{ID: "m2b", ConversationID: "c1", UserID: "u1", Role: "assistant", Sequence: 2, Bubble: 2, Content: strings.Repeat("早期回复乙", 6), CreatedAt: started.Add(time.Minute)},
		{ID: "m3", ConversationID: "c1", UserID: "u1", Role: "user", Sequence: 3, Bubble: 1, Content: "现在只讨论明天的安排", CreatedAt: started.Add(2 * time.Minute)},
	}
	builder := NewContextBuilder(store, 32, 80)
	builder.now = func() time.Time { return started.Add(3 * time.Minute) }
	result, err := builder.Build(ctx, "u1", "c1")
	if err != nil {
		t.Fatal(err)
	}
	if result.SummariesAdded != 1 || result.Summary == nil {
		t.Fatalf("summary result = %#v", result)
	}
	if result.Summary.StartSequence != 1 || result.Summary.EndSequence != 2 {
		t.Fatalf("summary range = %d-%d", result.Summary.StartSequence, result.Summary.EndSequence)
	}
	if result.Summary.TokenCount > 80 {
		t.Fatalf("summary token budget exceeded: %d", result.Summary.TokenCount)
	}
	if len(result.Messages) != 2 || result.Messages[0].Role != "system" || result.Messages[1].ID != "m3" {
		t.Fatalf("context messages = %#v", result.Messages)
	}
	if !strings.Contains(result.Messages[0].Content, "覆盖序号 1–2") {
		t.Fatalf("summary context = %q", result.Messages[0].Content)
	}
	latest, err := store.GetLatestSummary(ctx, "u1", "c1")
	if err != nil || latest.Version != 1 {
		t.Fatalf("latest summary = %#v, %v", latest, err)
	}
}

func TestContextBuilderAdvancesSummaryWithoutSplittingAssistantBubbles(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Now().UTC()
	store.conversations["c1"] = Conversation{ID: "c1", UserID: "u1"}
	store.messages["c1"] = []Message{
		{ID: "u1", Role: "user", Sequence: 1, Bubble: 1, Content: strings.Repeat("旧", 60), CreatedAt: now},
		{ID: "a1", Role: "assistant", Sequence: 2, Bubble: 1, Content: strings.Repeat("甲", 20), CreatedAt: now},
		{ID: "a2", Role: "assistant", Sequence: 2, Bubble: 2, Content: strings.Repeat("乙", 20), CreatedAt: now},
		{ID: "u2", Role: "user", Sequence: 3, Bubble: 1, Content: "最新问题", CreatedAt: now},
	}
	for index := range store.messages["c1"] {
		store.messages["c1"][index].ConversationID = "c1"
		store.messages["c1"][index].UserID = "u1"
	}
	result, err := NewContextBuilder(store, 58, 100).Build(ctx, "u1", "c1")
	if err != nil {
		t.Fatal(err)
	}
	assistantBubbles := 0
	for _, message := range result.Messages {
		if message.Sequence == 2 {
			assistantBubbles++
		}
	}
	if assistantBubbles != 2 {
		t.Fatalf("assistant group was split: %#v", result.Messages)
	}
	if result.Messages[len(result.Messages)-1].ID != "u2" {
		t.Fatalf("latest message missing: %#v", result.Messages)
	}
}

func TestEstimateTokensHandlesChineseAndLatin(t *testing.T) {
	if got := EstimateTokens("你好世界"); got != 4 {
		t.Fatalf("Chinese estimate = %d", got)
	}
	if got := EstimateTokens("abcdefgh"); got != 2 {
		t.Fatalf("Latin estimate = %d", got)
	}
}

func TestSummaryOversizedFactIsReportedInsteadOfCutMidSentence(t *testing.T) {
	builder := NewContextBuilder(NewMemoryStore(), 128, 64)
	text, _ := builder.summarize(context.Background(), nil, []Message{{ID: "long", Role: "user", Content: strings.Repeat("很长的项目背景", 100)}})
	var summary StructuredSummary
	if err := json.Unmarshal([]byte(text), &summary); err != nil {
		t.Fatal(err)
	}
	if len(summary.Facts) != 0 || summary.Omitted != 1 || EstimateTokens(text) > 64 {
		t.Fatalf("invalid bounded summary: %s", text)
	}
}
