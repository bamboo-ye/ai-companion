package httpserver

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/windcry1/ai-companion/internal/agent"
	"github.com/windcry1/ai-companion/internal/platform/config"
)

func TestPublicRunHidesAuditContentAfterSourceInvalidation(t *testing.T) {
	for _, outcome := range []string{"evidence_access_changed", "specialist_review_failed"} {
		output, _ := json.Marshal(map[string]any{"outcome": outcome, "response": "PRIVATE source", "arbitrations": []string{"PRIVATE reason"}, "observations": []string{"PRIVATE quote"}, "evidence_review": map[string]any{"status": "unresolved", "termination_reason": "evidence_access_changed"}})
		projected := publicAgentRun(agent.Run{ID: "run", Output: output})
		data, _ := json.Marshal(projected)
		if strings.Contains(string(data), "PRIVATE") || strings.Contains(string(data), "arbitrations") {
			t.Fatalf("audit leak: %s", data)
		}
		if !strings.Contains(string(data), "evidence_access_changed") {
			t.Fatal("lost source invalidation")
		}
	}
}

func TestPublicReviewProjectionDropsPrivateFieldsAndUnverifiedSuccess(t *testing.T) {
	for _, reason := range []string{"resolved", "source_version_changed", "snapshot_changed", "evidence_access_changed"} {
		for _, verified := range []bool{true, false} {
			output, _ := json.Marshal(map[string]any{
				"response": "PRIVATE answer", "observations": []string{"PRIVATE quote"},
				"evidence_review": map[string]any{
					"status": "resolved", "total_issues": 1, "resolved_issues": 1,
					"rounds": 2, "additional_reads": 1, "isolation_passed": true,
					"sources_verified": verified, "termination_reason": reason, "private_quote": "SECRET",
				},
			})
			public := publicAgentRun(agent.Run{Output: output})["output"].(map[string]any)
			review := public["evidence_review"].(map[string]any)
			data, _ := json.Marshal(review)
			if strings.Contains(string(data), "SECRET") {
				t.Fatal("private summary field leaked")
			}
			if reason != "resolved" || !verified {
				if review["status"] != "unresolved" || review["resolved_issues"] != 0 {
					t.Fatalf("unverified success: %s", data)
				}
			}
			if reason != "resolved" {
				data, _ = json.Marshal(public)
				if strings.Contains(string(data), "PRIVATE") {
					t.Fatal("invalidated source leaked")
				}
			}
		}
	}
}

func TestPublicReviewRejectsMalformedCounters(t *testing.T) {
	for _, number := range []any{-1, 0.5, 99, "1", nil} {
		output, _ := json.Marshal(map[string]any{"evidence_review": map[string]any{
			"total_issues": 1, "resolved_issues": number, "rounds": 1, "additional_reads": 0,
			"isolation_passed": true, "sources_verified": true,
		}})
		public := publicAgentRun(agent.Run{Output: output})["output"].(map[string]any)
		review, _ := public["evidence_review"].(map[string]any)
		if review != nil {
			t.Fatal("malformed summary was retained")
		}
	}
}

func TestEvidenceReviewHistoryEndpointIsOwnerScopedAndContentFree(t *testing.T) {
	server := New(config.Config{HTTPAddr: ":0", ServiceName: "test", Environment: "test", AuthTokenSecret: "review-history-secret-with-enough-entropy"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	registered := performJSON(t, server, http.MethodPost, "/v1/auth/register", "", map[string]any{
		"email": "review-history@example.com", "password": "correct-horse-battery", "display_name": "Review Test", "timezone": "Asia/Shanghai",
		"device": map[string]any{"device_key": "review-test", "name": "Review Test", "platform": "web"},
	})
	var account struct {
		AccessToken string `json:"access_token"`
		User        struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal(registered.Body.Bytes(), &account); err != nil || account.AccessToken == "" {
		t.Fatalf("register test account: %s", registered.Body.String())
	}
	output, _ := json.Marshal(map[string]any{
		"response": "PRIVATE reply", "observations": []string{"PRIVATE excerpt"}, "arbitrations": []string{"PRIVATE reason"},
		"evidence_review": map[string]any{"status": "resolved", "total_issues": 1, "resolved_issues": 1, "rounds": 1, "additional_reads": 0, "isolation_passed": true, "sources_verified": true, "termination_reason": "resolved", "secret": "PRIVATE"},
	})
	store := &agentHTTPStore{run: agent.Run{ID: "review-run", UserID: account.User.ID, ConversationID: "review-conversation", Status: "completed", Output: output}}
	server.SetAgentStore(store)
	url := "/v1/agent-runs/review-run/evidence-review"
	response := performJSON(t, server, http.MethodGet, url, account.AccessToken, nil)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("history response: %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "PRIVATE") || !strings.Contains(response.Body.String(), `"status":"resolved"`) {
		t.Fatalf("unsafe or missing summary: %s", response.Body.String())
	}
	if response = performJSON(t, server, http.MethodGet, url, "", nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous request: %d", response.Code)
	}
	store.run.UserID = "another-owner"
	if response = performJSON(t, server, http.MethodGet, url, account.AccessToken, nil); response.Code != http.StatusNotFound {
		t.Fatalf("cross-user request: %d", response.Code)
	}
}
