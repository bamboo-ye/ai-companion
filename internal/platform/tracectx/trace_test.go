package tracectx

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestWithIDNormalizesLegacyAndPreservesInvalidReplacement(t *testing.T) {
	ctx := WithID(context.Background(), "trace-1234567890abcdef")
	traceID := ID(ctx)
	if !Valid(traceID) || traceID == "trace-1234567890abcdef" {
		t.Fatalf("normalized trace id=%q", traceID)
	}
	if got := ID(WithID(ctx, "not valid")); got != traceID {
		t.Fatalf("invalid replacement should preserve parent trace, got %q", got)
	}
}

func TestHTTPTraceContextCreatesChildAndInjectsCanonicalHeaders(t *testing.T) {
	const upstream = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	header := make(http.Header)
	header.Set(TraceParentHeader, upstream)
	header.Set(TraceStateHeader, "vendor=value")
	ctx := ExtractHTTP(context.Background(), header)
	if got := ID(ctx); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("trace id=%q", got)
	}
	if got := SpanID(ctx); got == "00f067aa0ba902b7" || len(got) != 16 {
		t.Fatalf("server span id=%q", got)
	}
	response := make(http.Header)
	InjectHTTP(ctx, response)
	if got := response.Get(LegacyTraceHeader); got != ID(ctx) {
		t.Fatalf("legacy header=%q", got)
	}
	if got := response.Get(TraceParentHeader); !strings.HasPrefix(got, "00-"+ID(ctx)+"-") || got == upstream {
		t.Fatalf("traceparent=%q", got)
	}
	if got := response.Get(TraceStateHeader); got != "vendor=value" {
		t.Fatalf("tracestate=%q", got)
	}
}

func TestPropagationRejectsTraceIDMismatch(t *testing.T) {
	_, ok := FromPropagation(context.Background(), "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", "", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if ok {
		t.Fatal("expected mismatched trace identifiers to be rejected")
	}
}

func TestRunCorrelationAndDeterministicAgentTrace(t *testing.T) {
	ctx := WithRunID(context.Background(), "run-123")
	if got := RunID(ctx); got != "run-123" {
		t.Fatalf("run id=%q", got)
	}
	if got := RunID(WithRunID(ctx, "not valid")); got != "run-123" {
		t.Fatalf("invalid replacement should preserve parent run, got %q", got)
	}
	if got := AgentRunTraceID("test-run"); got != "2da20f1b3d100f4952a71cca0e687b68" {
		t.Fatalf("AgentRunTraceID()=%q", got)
	}
	if got := AgentRunTraceID("not valid"); got != "" {
		t.Fatalf("invalid run trace=%q", got)
	}
}
