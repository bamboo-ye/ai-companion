package opslog

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/windcry1/ai-companion/internal/platform/tracectx"
)

func TestCapturedLoggerRedactsSensitiveAttributesAndKeepsCorrelation(t *testing.T) {
	store := NewMemoryStore()
	var output bytes.Buffer
	logger := NewLogger(slog.NewJSONHandler(&output, nil), store, "agent-worker", "test")
	ctx := tracectx.WithID(context.Background(), "trace-1234567890")
	ctx = tracectx.WithRunID(ctx, "run-123")
	traceID := tracectx.ID(ctx)
	logger.ErrorContext(ctx, "model call failed for person@example.com with Bearer secret-token-value",
		"event", "agent.model.failed",
		"node", "compose",
		"error_code", "provider_timeout",
		"api_key", "must-not-leak",
		"details", map[string]any{"password": "nested-must-not-leak", "attempt": 2},
		"prompt_tokens", 42,
	)
	page, err := store.QuerySystemLogs(context.Background(), Filter{Since: time.Now().Add(-time.Minute), Until: time.Now().Add(time.Minute)})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	entry := page.Items[0]
	if entry.Event != "agent.model.failed" || entry.TraceID != traceID || entry.RunID != "run-123" || entry.AgentTraceID != traceID || entry.Node != "compose" || entry.ErrorCode != "provider_timeout" {
		t.Fatalf("entry=%+v", entry)
	}
	if string(entry.Attributes) == "" || contains(string(entry.Attributes), "must-not-leak") || !contains(string(entry.Attributes), "[REDACTED]") || !contains(string(entry.Attributes), "prompt_tokens") {
		t.Fatalf("attributes=%s", entry.Attributes)
	}
	stdout := output.String()
	for _, forbidden := range []string{"must-not-leak", "nested-must-not-leak", "person@example.com", "secret-token-value"} {
		if contains(stdout, forbidden) {
			t.Fatalf("stdout leaked %q: %s", forbidden, stdout)
		}
	}
	for _, required := range []string{`"service":"agent-worker"`, `"environment":"test"`, `"trace_id":"` + traceID + `"`, `"span_id":"` + tracectx.SpanID(ctx) + `"`, `"run_id":"run-123"`, `"agent_trace_id":"` + traceID + `"`, "[REDACTED]", "[REDACTED_EMAIL]"} {
		if !contains(stdout, required) {
			t.Fatalf("stdout missing %q: %s", required, stdout)
		}
	}
}

func TestMemoryStoreFiltersAndPaginates(t *testing.T) {
	store := NewMemoryStore()
	now := time.Now().UTC()
	for _, entry := range []Entry{
		{OccurredAt: now.Add(-2 * time.Minute), Service: "api", Level: "INFO", Event: "http.request", Message: "http request", TraceID: "trace-one", Attributes: []byte(`{}`)},
		{OccurredAt: now.Add(-time.Minute), Service: "agent-worker", Level: "ERROR", Event: "agent.failed", Message: "agent failed", RunID: "run-one", Attributes: []byte(`{"attempt":2}`)},
	} {
		if err := store.AppendSystemLog(context.Background(), entry); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.QuerySystemLogs(context.Background(), Filter{Since: now.Add(-time.Hour), Until: now.Add(time.Minute), Level: "error", RunID: "run-one", Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Summary.Errors != 1 || page.Items[0].Service != "agent-worker" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if len(page.Services) != 2 || page.Summary.Services != 1 {
		t.Fatalf("facets=%+v summary=%+v", page.Services, page.Summary)
	}
}

func contains(value, needle string) bool {
	for index := 0; index+len(needle) <= len(value); index++ {
		if value[index:index+len(needle)] == needle {
			return true
		}
	}
	return false
}
