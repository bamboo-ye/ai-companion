package opslog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestLokiStoreQueriesStructuredLogsAndKeepsHighCardinalityOutOfSelector(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	traceID := "4bf92f3577b34da6a3ce929d0e0e4736"
	runID := "run-loki-123"
	var received url.Values
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/loki/api/v1/query_range" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("X-Scope-OrgID") != "tenant-a" || r.Header.Get("Authorization") != "Bearer private-token" {
			t.Errorf("headers = %#v", r.Header)
		}
		received = r.URL.Query()
		payload := fmt.Sprintf(`{"status":"success","data":{"resultType":"streams","result":[{"stream":{"job":"ai-companion","service":"agent-worker","environment":"test","level":"ERROR"},"values":[[%q,%q],[%q,%q]]}]}}`,
			fmt.Sprint(now.Add(-time.Second).UnixNano()), `{"level":"ERROR","event":"agent.model.failed","msg":"provider timeout for person@example.com","trace_id":"`+traceID+`","run_id":"`+runID+`","node":"compose","api_key":"must-not-leak"}`,
			fmt.Sprint(now.Add(-2*time.Second).UnixNano()), `{"level":"ERROR","event":"agent.retry","msg":"provider retry","trace_id":"`+traceID+`","run_id":"`+runID+`"}`,
		)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewBufferString(payload)), Request: r}, nil
	})}

	store, err := NewLokiStore(LokiOptions{BaseURL: "https://loki.example.com", Environment: "test", TenantID: "tenant-a", BearerToken: "private-token", Timeout: time.Second, HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.QuerySystemLogs(context.Background(), Filter{
		Since: now.Add(-time.Hour), Until: now, Service: "agent-worker", Level: "error",
		Query: "provider", TraceID: traceID, RunID: runID, Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Source != "loki" || len(page.Items) != 1 || page.NextBeforeID == 0 || page.Summary.Total != 2 || page.Summary.Errors != 2 {
		t.Fatalf("page = %+v", page)
	}
	entry := page.Items[0]
	if entry.OccurredAt.IsZero() || entry.TraceID != traceID || entry.RunID != runID || entry.AgentTraceID != traceID || entry.Node != "compose" {
		t.Fatalf("entry = %+v", entry)
	}
	if strings.Contains(entry.Message, "person@example.com") || !strings.Contains(entry.Message, "[REDACTED_EMAIL]") || strings.Contains(string(entry.Attributes), "must-not-leak") {
		t.Fatalf("redaction failed: message=%q attributes=%s", entry.Message, entry.Attributes)
	}
	query := received.Get("query")
	selector := strings.SplitN(query, "}", 2)[0]
	for _, forbidden := range []string{"trace_id", "run_id", "agent_trace_id"} {
		if strings.Contains(selector, forbidden) {
			t.Fatalf("high-cardinality field %q was indexed in selector %q", forbidden, selector)
		}
	}
	for _, expected := range []string{`job="ai-companion"`, `environment="test"`, `service="agent-worker"`, `level="ERROR"`, `|= "provider"`, `trace_id = "` + traceID + `"`, `agent_trace_id = "` + traceID + `"`, `run_id = "` + runID + `"`} {
		if !strings.Contains(query, expected) {
			t.Fatalf("query missing %q: %s", expected, query)
		}
	}
	if received.Get("direction") != "backward" || received.Get("limit") != "2" || received.Get("start") == "" || received.Get("end") == "" {
		t.Fatalf("query values = %#v", received)
	}
}

func TestQueryFallbackStoreMarksPostgresFallbackAsDegraded(t *testing.T) {
	durable := NewMemoryStore()
	now := time.Now().UTC()
	if err := durable.AppendSystemLog(context.Background(), Entry{OccurredAt: now, Service: "api", Environment: "test", Level: "INFO", Event: "http.request", Message: "request complete", Attributes: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	store := NewQueryFallbackStore(durable, failingQueryStore{})
	page, err := store.QuerySystemLogs(context.Background(), Filter{Since: now.Add(-time.Minute), Until: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if !page.Degraded || page.Source != "memory" || len(page.Items) != 1 {
		t.Fatalf("page = %+v", page)
	}
}

type failingQueryStore struct{}

func (failingQueryStore) QuerySystemLogs(context.Context, Filter) (Page, error) {
	return Page{}, errors.New("collector unavailable")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
