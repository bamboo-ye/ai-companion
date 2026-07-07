package conversation

import (
	"context"
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
