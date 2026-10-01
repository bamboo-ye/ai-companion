package chattool

import (
	"context"
	"testing"

	"github.com/windcry1/ai-companion/internal/conversation"
	"github.com/windcry1/ai-companion/internal/knowledge"
)

func TestUnifiedKnowledgeToolScopeAndReadOnlyContract(t *testing.T) {
	e := New(nil, nil, nil, nil)
	ctx := context.Background()
	req := conversation.ToolRequest{UserID: "alice", Module: "work"}
	call := conversation.ModelToolCall{Name: "work_knowledge_search", Arguments: map[string]any{"query": "伴AI", "source_kinds": []string{"builtin"}}}
	result, err := e.ExecuteModelTool(ctx, req, call)
	if err != nil || !result.ReferenceOnly {
		t.Fatal(result, err)
	}
	data := result.Data.(knowledge.Result)
	if len(data.Hits) == 0 {
		t.Fatal("builtin provider unavailable")
	}
	ref := data.Hits[0].Ref
	read := conversation.ModelToolCall{Name: "work_knowledge_read", Arguments: map[string]any{"kind": ref.Kind, "source_id": ref.SourceID, "source_revision": ref.Revision, "content_hash": ref.ContentHash}}
	if result, err = e.ExecuteModelTool(ctx, req, read); err != nil || !result.ReferenceOnly {
		t.Fatal(result, err)
	}
	read.Arguments["offset"] = 0.5
	if _, err = e.ExecuteModelTool(ctx, req, read); err == nil {
		t.Fatal("fractional offset accepted")
	}
	call.Arguments["user_id"] = "victim"
	if _, err = e.ExecuteModelTool(ctx, req, call); err == nil {
		t.Fatal("model scope accepted")
	}
	delete(call.Arguments, "user_id")
	for _, module := range []string{"life", "companion", ""} {
		req.Module = module
		if _, err = e.ExecuteModelTool(ctx, req, call); err == nil {
			t.Fatal("module escaped", module)
		}
	}
}
