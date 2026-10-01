package chattool

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/windcry1/ai-companion/internal/conversation"
	"github.com/windcry1/ai-companion/internal/document"
	"github.com/windcry1/ai-companion/internal/skill"
)

func TestPresentationRevisionUsesLatestDeckInCurrentUserConversation(t *testing.T) {
	ctx := context.Background()
	store := skill.NewMemoryStore()
	now := time.Now()
	for i, seed := range []struct{ user, conversation, title string }{
		{"u1", "c1", "较早版本"}, {"u1", "c1", "真实项目"}, {"u1", "c2", "其他对话"}, {"u2", "c1", "其他账户"},
	} {
		data, _ := json.Marshal(map[string]any{"deck_spec": map[string]any{"title": seed.title, "audience": "工程团队", "slides": []any{map[string]any{"id": "overview"}}}})
		id := seed.title
		if err := store.CreateSkillRun(ctx, skill.Run{ID: id, CreateKey: id, UserID: seed.user, ConversationID: seed.conversation, SkillName: "office.pptx_generate", Status: "succeeded", Output: data, Files: []skill.GeneratedFile{{Name: "verified.pptx"}}, CreatedAt: now.Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	worker := &pdfWorker{}
	registry := skill.NewRegistry()
	if err := skill.RegisterBuiltins(registry); err != nil {
		t.Fatal(err)
	}
	if err := skill.RegisterOfficeSkills(registry, worker); err != nil {
		t.Fatal(err)
	}
	executor := New(nil, nil, document.NewService(document.NewMemoryStore(), document.NewMemoryBlobStore(), 20<<20), skill.NewService(store, skill.NewMemoryFileStore(), registry))
	arguments := map[string]any{"title": "模型重写的标题", "audience": "模型改写", "style": "简洁", "brief": "", "slide_count": 8, "revision_of": "latest", "style_prompt": "浅蓝白课堂图解", "deck_spec": map[string]any{"title": "伪造内容"}}
	result, err := executor.ExecuteModelTool(ctx, conversation.ToolRequest{UserID: "u1", ConversationID: "c1", MessageID: "rev-1", Module: "work"}, conversation.ModelToolCall{Name: "work_generate_pptx", Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	if worker.input["title"] != "真实项目" || worker.input["audience"] != "工程团队" || worker.input["slide_count"] != float64(1) && worker.input["slide_count"] != 1 || worker.input["style_prompt"] != "浅蓝白课堂图解" || worker.input["revision_of"] != nil {
		t.Fatalf("revision did not preserve authenticated source: %#v; result=%#v", worker.input, result)
	}
	for _, conversationID := range []string{"", "no-deck"} {
		_, err = executor.ExecuteModelTool(ctx, conversation.ToolRequest{UserID: "u1", ConversationID: conversationID, MessageID: "missing", Module: "work"}, conversation.ModelToolCall{Name: "work_generate_pptx", Arguments: arguments})
		if err == nil {
			t.Fatal("revision without a current conversation deck must fail")
		}
	}
	arguments["revision_of"], arguments["project_name"] = "", ""
	arguments["brief"] = "## 课程目标\n- 学会解释概念\n## 教学内容\n- 用例子理解知识"
	arguments["slide_count"] = float64(1)
	_, err = executor.ExecuteModelTool(ctx, conversation.ToolRequest{UserID: "u1", ConversationID: "new-deck", MessageID: "fresh", Module: "work"}, conversation.ModelToolCall{Name: "work_generate_pptx", Arguments: arguments})
	if err != nil || worker.input["deck_spec"] != nil || worker.input["brief"] != arguments["brief"] {
		t.Fatalf("fresh request must use supplied content, not a previous or forged deck: %#v %v", worker.input, err)
	}
}
