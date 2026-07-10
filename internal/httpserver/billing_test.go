package httpserver

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/windcry1/ai-companion/internal/billing"
	"github.com/windcry1/ai-companion/internal/platform/config"
)

func TestBillingSummaryReturnsImplicitFreePlanAndUsage(t *testing.T) {
	server := New(config.Config{HTTPAddr: ":0", ServiceName: "test", Environment: "test", AuthTokenSecret: "billing-summary-secret-with-enough-entropy"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	store := billing.NewMemoryStore()
	server.SetBillingStore(store)
	token, userID := registerBillingUser(t, server, "billing-summary@example.com", "billing-summary")
	store.SetUsage(userID, 2, 1, []time.Time{time.Now().UTC()})

	response := performJSON(t, server, http.MethodGet, "/v1/billing/me", token, nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"code":"free"`) || !strings.Contains(response.Body.String(), `"resource":"documents"`) || !strings.Contains(response.Body.String(), `"used":2`) || !strings.Contains(response.Body.String(), `"resource":"skill_runs"`) || !strings.Contains(response.Body.String(), `"resource":"workspaces"`) {
		t.Fatalf("billing summary=%d %s", response.Code, response.Body.String())
	}
}

func TestFreePlanQuotaBlocksResourceCreation(t *testing.T) {
	server := New(config.Config{HTTPAddr: ":0", ServiceName: "test", Environment: "test", AuthTokenSecret: "billing-quota-secret-with-enough-entropy"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	store := billing.NewMemoryStore()
	server.SetBillingStore(store)
	token, userID := registerBillingUser(t, server, "billing-quota@example.com", "billing-quota")
	now := time.Now().UTC()
	store.SetUsage(userID, 10, 3, repeatTimes(now, 20))

	documentUpload := performUpload(t, server, token, "quota.txt", []byte("quota exhausted"))
	if documentUpload.Code != http.StatusPaymentRequired || !strings.Contains(documentUpload.Body.String(), `"resource":"documents"`) {
		t.Fatalf("document quota=%d %s", documentUpload.Code, documentUpload.Body.String())
	}
	skillRun := performSkillRequest(t, server, http.MethodPost, "/v1/skills/office.translate/runs", token, "quota-skill", map[string]any{"input": map[string]any{"text": "你好", "target_language": "English"}})
	if skillRun.Code != http.StatusPaymentRequired || !strings.Contains(skillRun.Body.String(), `"resource":"skill_runs"`) {
		t.Fatalf("skill quota=%d %s", skillRun.Code, skillRun.Body.String())
	}
	workspace := performJSON(t, server, http.MethodPost, "/v1/workspaces", token, map[string]string{"name": "超限空间"})
	if workspace.Code != http.StatusPaymentRequired || !strings.Contains(workspace.Body.String(), `"resource":"workspaces"`) {
		t.Fatalf("workspace quota=%d %s", workspace.Code, workspace.Body.String())
	}
}

func registerBillingUser(t *testing.T, server *Server, email, key string) (string, string) {
	t.Helper()
	response := performJSON(t, server, http.MethodPost, "/v1/auth/register", "", map[string]any{
		"email": email, "password": "correct-horse-battery", "display_name": key, "timezone": "Asia/Shanghai",
		"device": map[string]any{"device_key": key, "name": key, "platform": "web"},
	})
	var tokens struct {
		AccessToken string `json:"access_token"`
		User        struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &tokens); err != nil {
		t.Fatal(err)
	}
	return tokens.AccessToken, tokens.User.ID
}

func repeatTimes(value time.Time, count int) []time.Time {
	items := make([]time.Time, count)
	for index := range items {
		items[index] = value
	}
	return items
}
