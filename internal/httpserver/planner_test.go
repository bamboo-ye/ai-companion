package httpserver

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/windcry1/ai-companion/internal/platform/config"
)

func TestPlanAndReminderConfirmationAPI(t *testing.T) {
	server := New(config.Config{HTTPAddr: ":0", ServiceName: "test", Environment: "test", AuthTokenSecret: "planner-test-secret-with-enough-entropy"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	register := performJSON(t, server, http.MethodPost, "/v1/auth/register", "", map[string]any{"email": "planner@example.com", "password": "correct-horse-battery", "display_name": "计划用户", "timezone": "Asia/Shanghai", "device": map[string]any{"device_key": "planner", "name": "Planner", "platform": "web"}})
	var tokens struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.Unmarshal(register.Body.Bytes(), &tokens)
	plan := performJSON(t, server, http.MethodPost, "/v1/plans", tokens.AccessToken, map[string]any{"title": "明日计划", "local_date": "2026-07-04", "items": []map[string]any{{"title": "提交报告", "priority": "high", "estimated_minutes": 45, "source": "memo"}}})
	if plan.Code != http.StatusCreated || !strings.Contains(plan.Body.String(), `"title":"提交报告"`) {
		t.Fatalf("plan=%d %s", plan.Code, plan.Body.String())
	}
	character := createCharacter(t, server, tokens.AccessToken, map[string]any{"name": "小棉", "personality": "温柔", "speech_style": "短句"})
	convResponse := performJSON(t, server, http.MethodPost, "/v1/conversations", tokens.AccessToken, map[string]string{"character_id": character.Character.ID})
	var conv struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(convResponse.Body.Bytes(), &conv)
	chat := performJSON(t, server, http.MethodPost, "/v1/conversations/"+conv.ID+"/messages", tokens.AccessToken, map[string]string{"content": "明早九点提醒我交报告"})
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("load timezone: %v", err)
	}
	expectedDue := time.Now().In(location).AddDate(0, 0, 1).Format("2006-01-02") + " 09:00"
	if chat.Code != http.StatusAccepted || !strings.Contains(chat.Body.String(), `"reminder_candidate"`) || !strings.Contains(chat.Body.String(), `"local_due":"`+expectedDue+`"`) {
		t.Fatalf("chat=%d %s", chat.Code, chat.Body.String())
	}
	var accepted struct {
		Reminder struct {
			ID string `json:"id"`
		} `json:"reminder_candidate"`
	}
	_ = json.Unmarshal(chat.Body.Bytes(), &accepted)
	confirmed := performReminderConfirm(t, server, tokens.AccessToken, accepted.Reminder.ID, "confirm-report")
	if confirmed.Code != http.StatusCreated || !strings.Contains(confirmed.Body.String(), `"reminder":{"id"`) || !strings.Contains(confirmed.Body.String(), `"status":"active"`) || !strings.Contains(confirmed.Body.String(), `"deduplicated":false`) {
		t.Fatalf("confirm=%d %s", confirmed.Code, confirmed.Body.String())
	}
	deduplicated := performReminderConfirm(t, server, tokens.AccessToken, accepted.Reminder.ID, "confirm-report")
	if deduplicated.Code != http.StatusOK || !strings.Contains(deduplicated.Body.String(), `"deduplicated":true`) {
		t.Fatalf("deduplicated confirm=%d %s", deduplicated.Code, deduplicated.Body.String())
	}
	denied := performJSON(t, server, http.MethodPost, "/v1/reminders/"+accepted.Reminder.ID+"/system-sync", tokens.AccessToken, map[string]string{"status": "permission_denied", "provider": "ios_eventkit", "error_code": "permission_denied"})
	if denied.Code != http.StatusOK || !strings.Contains(denied.Body.String(), `"system_sync_status":"permission_denied"`) || !strings.Contains(denied.Body.String(), `"status":"active"`) {
		t.Fatalf("denied=%d %s", denied.Code, denied.Body.String())
	}
	listed := performJSON(t, server, http.MethodGet, "/v1/reminders", tokens.AccessToken, nil)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), accepted.Reminder.ID) {
		t.Fatalf("list=%d %s", listed.Code, listed.Body.String())
	}
	completed := performJSON(t, server, http.MethodPost, "/v1/reminders/"+accepted.Reminder.ID+"/complete", tokens.AccessToken, nil)
	if completed.Code != http.StatusNoContent {
		t.Fatalf("complete=%d %s", completed.Code, completed.Body.String())
	}
	ambiguous := performJSON(t, server, http.MethodPost, "/v1/reminders/candidates", tokens.AccessToken, map[string]string{"text": "明早提醒我交报告"})
	var fuzzy struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(ambiguous.Body.Bytes(), &fuzzy)
	blocked := performReminderConfirm(t, server, tokens.AccessToken, fuzzy.ID, "fuzzy")
	if blocked.Code != http.StatusConflict {
		t.Fatalf("fuzzy confirm=%d %s", blocked.Code, blocked.Body.String())
	}
}

func performReminderConfirm(t *testing.T, server *Server, token, reminderID, key string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/reminders/"+reminderID+"/confirm", bytes.NewReader([]byte(`{}`)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	return response
}
