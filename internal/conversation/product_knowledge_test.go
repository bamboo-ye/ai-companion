package conversation

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/windcry1/ai-companion/internal/character"
)

type helpTools struct{}

func (helpTools) Execute(context.Context, ToolRequest) (ToolResult, error) {
	return ToolResult{}, errors.New("legacy rules must not run")
}
func (helpTools) ModelTools(ToolRequest) []ModelToolDefinition {
	return []ModelToolDefinition{{Name: "product_knowledge_search"}}
}
func (helpTools) ExecuteModelTool(context.Context, ToolRequest, ModelToolCall) (ToolResult, error) {
	return ToolResult{Handled: true, ReferenceOnly: true, ToolName: "product_knowledge_search", Response: "检索完成", Data: map[string]string{"body": "独立检索的完整操作说明"}}, nil
}

type helpProvider struct {
	history []Message
	calls   int
}

func (p *helpProvider) GenerateWithTools(context.Context, character.Character, []Message, []ModelToolDefinition) (ModelToolTurn, Usage, error) {
	return ModelToolTurn{Call: &ModelToolCall{Name: "product_knowledge_search"}}, Usage{}, nil
}
func (p *helpProvider) Generate(_ context.Context, _ character.Character, history []Message) (string, Usage, error) {
	p.history = history
	p.calls++
	return "依据产品指南完成回答。", Usage{}, nil
}

func TestLegacyKnowledgeLookupContinuesToGroundedAnswer(t *testing.T) {
	ctx := context.Background()
	characters := character.NewService(character.NewMemoryStore())
	persona, _, err := characters.Create(ctx, "user", character.Input{Name: "指南助手", Module: "companion"})
	if err != nil {
		t.Fatal(err)
	}
	provider := &helpProvider{}
	service := NewService(NewMemoryStore(), characters, provider)
	service.SetAsyncDispatch(true)
	service.timeBetweenBubbles = 0
	service.SetToolExecutor(helpTools{})
	conv, err := service.Create(ctx, "user", persona.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, job, err := service.Send(ctx, "user", conv.ID, "如何使用知识库")
	if err != nil {
		t.Fatal(err)
	}
	service.run(ctx, "user", job.ID)
	if provider.calls != 1 {
		t.Fatal("retrieval stopped before answering")
	}
	found := false
	for _, m := range provider.history {
		if strings.Contains(m.Content, "独立检索的完整操作说明") {
			if m.Role != "user" {
				t.Fatal("tool reference elevated")
			}
			found = true
		}
	}
	if !found || provider.history[len(provider.history)-1].Content != "如何使用知识库" {
		t.Fatal("lost evidence/current question")
	}
	messages, _ := service.Messages(ctx, "user", conv.ID, 0, 0, 20)
	if messages[len(messages)-1].Content != "依据产品指南完成回答。" {
		t.Fatal("raw retrieval status used as answer")
	}
}
