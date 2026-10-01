package httpserver

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/windcry1/ai-companion/internal/agent"
	"github.com/windcry1/ai-companion/internal/platform/config"
)

func TestAgentEvidenceRequiresServiceIdentityAndPersistedRunFence(t *testing.T) {
	const token = "evidence-http-test-token"
	s := New(config.Config{HTTPAddr: ":0", Environment: "test", AgentGatewayToken: token,
		AgentConfirmationSecret: "agent-evidence-confirmation-secret"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	input, _ := json.Marshal(map[string]string{"message_id": "message-1", "text": "核对证据"})
	expires := time.Now().Add(time.Hour)
	store := &agentHTTPStore{run: agent.Run{ID: "run-evidence", UserID: "alice", Module: "work", Status: "running", Input: input,
		Revision: 1, LeaseOwner: "worker", LeaseExpiresAt: &expires, DeadlineAt: expires}}
	s.SetAgentStore(store)
	path := "/internal/v1/agent/runs/run-evidence/evidence/validate"
	for _, c := range []struct {
		token          string
		revision, want int
	}{{"", 1, http.StatusUnauthorized}, {token, 0, http.StatusBadRequest}, {token, 2, http.StatusConflict}, {token, 1, http.StatusOK}} {
		r := performAgentJSON(t, s, http.MethodPost, path, c.token, c.revision, map[string]any{"references": []any{}})
		if r.Code != c.want {
			t.Fatalf("got=%d want=%d body=%s", r.Code, c.want, r.Body.String())
		}
	}
	r := performAgentJSON(t, s, http.MethodPost, path, token, 1, map[string]any{"references": []any{map[string]string{"source_id": "other-user-document", "text": "secret"}}})
	if r.Code != http.StatusConflict {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	store.run.Module = "life"
	r = performAgentJSON(t, s, http.MethodPost, path, token, 1, map[string]any{"references": []any{}})
	if r.Code != http.StatusForbidden {
		t.Fatalf("non-work validation: %d", r.Code)
	}
}
