package chattool

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	"github.com/windcry1/ai-companion/internal/contextengine"
	"github.com/windcry1/ai-companion/internal/conversation"
	"github.com/windcry1/ai-companion/internal/productknowledge"
)

func productKnowledgeTools() []conversation.ModelToolDefinition {
	return []conversation.ModelToolDefinition{
		{Name: "product_knowledge_search", Description: "检索伴AI内置项目介绍、用户指南、后台指南和部署说明；所有模块均可解释产品用法。每项包含完整章节正文及来源，过长正文可用 read 读取。资料不表示用户数据或已执行操作；咨询使用方法不要发起业务写入。", Parameters: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string", "minLength": 2, "maxLength": 1000}}, "required": []string{"query"}, "additionalProperties": false}, Repeatable: true, IdentityFields: []string{"query"}},
		{Name: "product_knowledge_read", Description: "读取内置知识完整章节，page_id 来自内置知识引用或检索结果。has_more=true 时用 next_offset 继续读取；links 是同章节其他部分。按需补查以覆盖相关步骤和限制，引用来源。", Parameters: map[string]any{"type": "object", "properties": map[string]any{"page_id": map[string]any{"type": "string", "maxLength": 80}, "offset": map[string]any{"type": "integer", "minimum": 0, "maximum": 1000000}}, "required": []string{"page_id"}, "additionalProperties": false}, Repeatable: true, IdentityFields: []string{"page_id", "offset"}},
	}
}

func (e *Executor) executeProductKnowledge(_ context.Context, request conversation.ToolRequest, call conversation.ModelToolCall) (conversation.ToolResult, error) {
	if request.UserID == "" || (request.Module != "companion" && request.Module != "life" && request.Module != "work") {
		return conversation.ToolResult{}, fmt.Errorf("product knowledge is unavailable for this request")
	}
	var data any
	switch call.Name {
	case "product_knowledge_search":
		query, _ := call.Arguments["query"].(string)
		hits, err := productknowledge.Search(query, 5)
		if err != nil {
			return conversation.ToolResult{}, err
		}
		// Give complete short guides rather than title-only hits. Long chapters
		// carry an explicit continuation cursor, not a silent truncation.
		items := make([]productknowledge.ReadResult, 0, len(hits))
		used := 0
		for _, hit := range hits {
			part, _ := productknowledge.ReadPart(hit.Page.ID, 0)
			encoded, _ := json.Marshal(part)
			cost := contextengine.EstimateTokens(string(encoded)) + 4
			if used+cost > 5500 {
				continue
			}
			used += cost
			items = append(items, part)
		}
		data = map[string]any{"revision": productknowledge.Revision(), "items": items, "omitted": len(hits) - len(items)}
	case "product_knowledge_read":
		id, _ := call.Arguments["page_id"].(string)
		offset := 0
		if raw, exists := call.Arguments["offset"]; exists {
			switch value := raw.(type) {
			case int:
				offset = value
			case float64:
				if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1000000 || math.Trunc(value) != value {
					return conversation.ToolResult{}, productknowledge.ErrValidation
				}
				offset = int(value)
			default:
				return conversation.ToolResult{}, productknowledge.ErrValidation
			}
		}
		part, err := productknowledge.ReadPart(id, offset)
		if err != nil {
			return conversation.ToolResult{}, err
		}
		data = part
	default:
		return conversation.ToolResult{}, productknowledge.ErrValidation
	}
	return conversation.ToolResult{Handled: true, ReferenceOnly: true, ToolName: call.Name, Response: "已取得内置项目参考资料，请依据来源完整说明相关用法与限制；没有业务操作被执行。", Data: data}, nil
}
