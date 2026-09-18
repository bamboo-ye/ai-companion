package chattool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/windcry1/ai-companion/internal/contextengine"
	"github.com/windcry1/ai-companion/internal/conversation"
)

func TestProductKnowledgeAvailableAcrossModulesWithoutPrivateToolAccess(t *testing.T) {
	executor := New(nil, nil, nil, nil)
	for _, module := range []string{"companion", "life", "work"} {
		req := conversation.ToolRequest{UserID: "user", Module: module, Text: "怎么使用Wiki"}
		found := map[string]bool{}
		for _, def := range executor.ModelTools(req) {
			found[def.Name] = true
		}
		if !found["product_knowledge_search"] || !found["product_knowledge_read"] {
			t.Fatal("missing help tools", module)
		}
		if module != "work" && found["work_wiki_read"] {
			t.Fatal("help granted private wiki tool", module)
		}
		result, err := executor.ExecuteModelTool(context.Background(), req, conversation.ModelToolCall{Name: "product_knowledge_search", Arguments: map[string]any{"query": "如何上传文档到知识库"}})
		if err != nil || !result.ReferenceOnly || result.Confirmation != nil {
			t.Fatalf("%s: %v %+v", module, err, result)
		}
		encoded, _ := json.Marshal(result.Data)
		if !strings.Contains(string(encoded), "builtin-user-documents") || contextengine.EstimateTokens(string(encoded)) > 6000 {
			t.Fatal("incomplete/oversized help result")
		}
		if _, err := executor.ExecuteModelTool(context.Background(), req, conversation.ModelToolCall{Name: "product_knowledge_read", Arguments: map[string]any{"page_id": "builtin-user-welcome", "offset": 1.5}}); err == nil {
			t.Fatal("fractional offset accepted")
		}
		if _, err := executor.ExecuteModelTool(context.Background(), req, conversation.ModelToolCall{Name: "product_knowledge_read", Arguments: map[string]any{"page_id": "tenant-private-page"}}); err == nil {
			t.Fatal("private read accepted")
		}
	}
}
