package httpserver

import (
	"testing"

	"github.com/windcry1/ai-companion/internal/controlplane"
)

func TestBuildAgentNodeDebugFocusesEvidenceAndUpstreamOutputs(t *testing.T) {
	compilation := controlplane.AgentDefinitionCompilation{Definition: controlplane.AgentDefinitionPayload{
		Nodes: []controlplane.AgentNodeConfig{
			{Key: "route", Type: "router"},
			{Key: "answer", Type: "model", ModelRole: "responder", PromptTemplate: "Answer safely"},
		},
		Edges: []controlplane.AgentEdgeConfig{{From: "route", To: "answer"}},
	}}
	report := controlplane.AgentSandboxReport{
		NodeOutputs: map[string]any{"route": map[string]any{"condition": "direct"}, "answer": map[string]any{"response": "ok"}},
		NodeTrace: []map[string]any{
			{"node": "studio__route", "status": "succeeded"},
			{"node": "studio__answer", "status": "succeeded", "duration_ms": float64(12)},
		},
		ModelCalls: []map[string]any{{"graph_node": "studio__answer", "role": "responder"}},
		ToolCalls:  []map[string]any{{"node": "other", "tool_name": "ignored"}},
	}
	debug, ok := buildAgentNodeDebug(compilation, report, map[string]any{"module": "work", "user_message": "synthetic"}, "answer")
	if !ok || debug["status"] != "succeeded" {
		t.Fatalf("buildAgentNodeDebug() = %#v, %v", debug, ok)
	}
	input := debug["input"].(map[string]any)
	upstream := input["upstream_outputs"].(map[string]any)
	if len(upstream) != 1 || len(debug["trace"].([]map[string]any)) != 1 || len(debug["model_calls"].([]map[string]any)) != 1 || len(debug["tool_calls"].([]map[string]any)) != 0 {
		t.Fatalf("focused debug evidence = %#v", debug)
	}
	if _, ok = buildAgentNodeDebug(compilation, report, nil, "missing"); ok {
		t.Fatal("unknown debug node was accepted")
	}
}
