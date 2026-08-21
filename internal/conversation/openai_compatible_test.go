package conversation

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/windcry1/ai-companion/internal/character"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestOpenAICompatibleProvider(t *testing.T) {
	provider := NewOpenAICompatibleProvider("https://model.invalid/v1", "secret", "test-model", time.Second, 1_000_000, 2_000_000)
	provider.Client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("missing authorization")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"温柔回应。再给一个建议。"}}],"usage":{"prompt_tokens":12,"completion_tokens":8}}`))}, nil
	})}
	text, usage, err := provider.Generate(context.Background(), character.Character{Name: "小棉", Relationship: "朋友"}, []Message{{Role: "user", Content: "你好"}})
	if err != nil {
		t.Fatal(err)
	}
	if text == "" || usage.InputTokens != 12 || usage.OutputTokens != 8 || usage.EstimatedCostMicros != 28 {
		t.Fatalf("unexpected response: %q %#v", text, usage)
	}
}

func TestOpenAICompatibleProviderReturnsNativeToolCall(t *testing.T) {
	provider := NewOpenAICompatibleProvider("https://model.invalid/v1", "secret", "tool-model", time.Second, 0, 0)
	provider.Client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body struct {
			Temperature       *float64 `json:"temperature"`
			ToolChoice        string   `json:"tool_choice"`
			ParallelToolCalls *bool    `json:"parallel_tool_calls"`
			Tools             []struct {
				Type     string `json:"type"`
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.Temperature != nil || body.ToolChoice != "required" || body.ParallelToolCalls != nil || len(body.Tools) != 1 || body.Tools[0].Function.Name != "life_query_today_plan" {
			t.Fatalf("tool request = %#v", body)
		}
		response := `{"model":"tool-model","choices":[{"message":{"content":null,"tool_calls":[{"id":"call-1","type":"function","function":{"name":"life_query_today_plan","arguments":"{}"}}]}}],"usage":{"prompt_tokens":15,"completion_tokens":4}}`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(response))}, nil
	})}
	turn, usage, err := provider.GenerateWithTools(context.Background(), character.Character{Name: "小满", Module: "life"}, []Message{{Role: "user", Content: "我今天的计划是什么"}}, []ModelToolDefinition{{
		Name: "life_query_today_plan", Description: "查询真实今日计划", Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if turn.Call == nil || turn.Call.ID != "call-1" || turn.Call.Name != "life_query_today_plan" || len(turn.Call.Arguments) != 0 || usage.InputTokens != 15 || usage.OutputTokens != 4 {
		t.Fatalf("turn=%#v usage=%#v", turn, usage)
	}
}

func TestOpenRouterProviderUsesOrderedFallbacksAndActualUsage(t *testing.T) {
	provider := NewOpenRouterProvider(OpenRouterOptions{
		BaseURL: "https://openrouter.ai/api/v1", APIKey: "secret", Models: []string{"openai/gpt-5-mini", "openai/gpt-4o-mini"},
		Timeout: time.Second, MaxTokens: 768, DataCollection: "deny", ZDRRequired: false, ReasoningEffort: "minimal", ReasoningExclude: true,
		ProviderSort: "price", AllowProviderFallbacks: true, RequireParameters: true, MaxPromptPrice: 0.3, MaxCompletionPrice: 2.5,
		HTTPReferer: "https://companion.example.com", AppTitle: "AI Companion",
	})
	provider.Client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("HTTP-Referer") != "https://companion.example.com" || request.Header.Get("X-OpenRouter-Title") != "AI Companion" {
			t.Errorf("missing OpenRouter attribution headers")
		}
		var body struct {
			Models      []string `json:"models"`
			MaxTokens   int      `json:"max_tokens"`
			Temperature *float64 `json:"temperature"`
			Provider    struct {
				DataCollection string             `json:"data_collection"`
				ZDR            bool               `json:"zdr"`
				Sort           string             `json:"sort"`
				AllowFallbacks bool               `json:"allow_fallbacks"`
				RequireParams  bool               `json:"require_parameters"`
				MaxPrice       map[string]float64 `json:"max_price"`
			} `json:"provider"`
			Reasoning struct {
				Effort  string `json:"effort"`
				Exclude bool   `json:"exclude"`
			} `json:"reasoning"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if strings.Join(body.Models, ",") != "openai/gpt-5-mini,openai/gpt-4o-mini" || body.MaxTokens != 768 || body.Temperature != nil {
			t.Fatalf("unexpected routing request: %#v", body)
		}
		if body.Provider.DataCollection != "deny" || body.Provider.ZDR || body.Provider.Sort != "price" ||
			!body.Provider.AllowFallbacks || !body.Provider.RequireParams || body.Provider.MaxPrice["prompt"] != 0.3 || body.Provider.MaxPrice["completion"] != 2.5 {
			t.Fatalf("unexpected provider policy: %#v", body.Provider)
		}
		if body.Reasoning.Effort != "minimal" || !body.Reasoning.Exclude {
			t.Fatalf("unexpected reasoning policy: %#v", body.Reasoning)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"model":"openai/gpt-4o-mini","choices":[{"message":{"content":"回退成功"}}],"usage":{"prompt_tokens":20,"completion_tokens":10,"cost":0.0000004}}`))}, nil
	})}

	text, usage, err := provider.Generate(context.Background(), character.Character{Name: "小棉", Relationship: "朋友"}, []Message{{Role: "user", Content: "你好"}})
	if err != nil {
		t.Fatal(err)
	}
	if text != "回退成功" || usage.Provider != "openrouter" || usage.Model != "openai/gpt-4o-mini" || usage.EstimatedCostMicros != 1 {
		t.Fatalf("unexpected response: %q %#v", text, usage)
	}
}
