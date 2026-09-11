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

func TestDevelopmentQuotaBypassAllowsResourceCreationAndReportsUnlimited(t *testing.T) {
	server := New(config.Config{HTTPAddr: ":0", ServiceName: "test", Environment: "development", AuthTokenSecret: "billing-bypass-secret-with-enough-entropy", BillingQuotaDisabled: true}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	store := billing.NewMemoryStore()
	server.SetBillingStore(store)
	token, userID := registerBillingUser(t, server, "billing-bypass@example.com", "billing-bypass")
	store.SetUsage(userID, 10, 3, repeatTimes(time.Now().UTC(), 20))

	documentUpload := performUpload(t, server, token, "unlimited.txt", []byte("quota bypassed"))
	if documentUpload.Code != http.StatusAccepted {
		t.Fatalf("document upload=%d %s", documentUpload.Code, documentUpload.Body.String())
	}

	response := performJSON(t, server, http.MethodGet, "/v1/billing/me", token, nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"limit":-1`) || !strings.Contains(response.Body.String(), `"remaining":-1`) {
		t.Fatalf("billing summary=%d %s", response.Code, response.Body.String())
	}
}

func TestOperatorCanInspectAndAdjustUnifiedUsageLedger(t *testing.T) {
	server := New(config.Config{HTTPAddr: ":0", ServiceName: "test", Environment: "development", AuthTokenSecret: "billing-ops-secret-with-enough-entropy", OperatorToken: "billing-ops-token"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	store := billing.NewMemoryStore()
	server.SetBillingStore(store)
	_, userID := registerBillingUser(t, server, "billing-ledger@example.com", "billing-ledger")
	store.SetExtendedUsage(userID, []time.Time{time.Now().UTC()}, map[time.Time]int{time.Now().UTC(): 650_000})

	created := performOperatorJSON(t, server, http.MethodPost, "/v1/ops/billing/users/"+userID+"/adjustments", "billing-ops-token", "finance-admin", map[string]any{
		"resource": "model_cost_micros", "delta": -150_000, "reason": "service credit ticket FIN-42",
	})
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"adjustment"`) || !strings.Contains(created.Body.String(), `"used":500000`) {
		t.Fatalf("create adjustment=%d %s", created.Code, created.Body.String())
	}
	listed := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/billing/users/"+userID+"/adjustments", "billing-ops-token", "finance-admin", nil)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"actor":"finance-admin"`) || !strings.Contains(listed.Body.String(), `FIN-42`) {
		t.Fatalf("list adjustments=%d %s", listed.Code, listed.Body.String())
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
