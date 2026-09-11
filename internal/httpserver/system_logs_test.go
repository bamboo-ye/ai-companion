package httpserver

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/windcry1/ai-companion/internal/opslog"
	"github.com/windcry1/ai-companion/internal/platform/config"
)

func TestOperatorSystemLogsFiltersAndProtectsAccess(t *testing.T) {
	server := New(config.Config{HTTPAddr: ":0", ServiceName: "test", Environment: "test", AuthTokenSecret: "system-log-secret-with-enough-entropy", OperatorToken: "ops-token"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	store := opslog.NewMemoryStore()
	now := time.Now().UTC()
	_ = store.AppendSystemLog(context.Background(), opslog.Entry{
		OccurredAt: now.Add(-time.Minute), Service: "ai-companion-agent-worker", Environment: "test", Level: "ERROR",
		Event: "agent.execution.failed", Message: "Agent execution failed", TraceID: "trace-1234567890", RunID: "run-123", ErrorCode: "provider_timeout", Attributes: []byte(`{"attempt":2}`),
	})
	server.SetSystemLogStore(store)

	unauthorized := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/logs", "wrong-token", "", nil)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized=%d %s", unauthorized.Code, unauthorized.Body.String())
	}
	response := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/logs?level=error&run_id=run-123&limit=25", "ops-token", "", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"event":"agent.execution.failed"`) || !strings.Contains(response.Body.String(), `"errors":1`) {
		t.Fatalf("response=%d %s", response.Code, response.Body.String())
	}
	invalid := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/logs?since=not-a-time", "ops-token", "", nil)
	if invalid.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid=%d %s", invalid.Code, invalid.Body.String())
	}
}
