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
	plan := performJSON(t, server, http.MethodPost, "/v1/plans", tokens.AccessToken, map[string]any{"title": "明日计划", "local_date": "2099-07-04", "items": []map[string]any{{"title": "提交报告", "priority": "high", "estimated_minutes": 45, "source": "memo"}}})
	if plan.Code != http.StatusCreated || !strings.Contains(plan.Body.String(), `"title":"提交报告"`) {
		t.Fatalf("plan=%d %s", plan.Code, plan.Body.String())
	}
	var createdPlan struct {
		Items []struct {
			ID        string    `json:"id"`
			UpdatedAt time.Time `json:"updated_at"`
		} `json:"items"`
	}
	if err := json.Unmarshal(plan.Body.Bytes(), &createdPlan); err != nil || len(createdPlan.Items) != 1 {
		t.Fatalf("decode plan: %#v err=%v", createdPlan, err)
	}
	scheduledPlanItem := performJSON(t, server, http.MethodPost, "/v1/plans/today/items/"+createdPlan.Items[0].ID+"/schedule", tokens.AccessToken, map[string]any{"starts_at": "2099-07-04T07:30:00Z", "expected_updated_at": createdPlan.Items[0].UpdatedAt})
	if scheduledPlanItem.Code != http.StatusOK || !strings.Contains(scheduledPlanItem.Body.String(), `"starts_at":"2099-07-04T07:30:00Z"`) {
		t.Fatalf("schedule plan item=%d %s", scheduledPlanItem.Code, scheduledPlanItem.Body.String())
	}
	completedPlanItem := performJSON(t, server, http.MethodPost, "/v1/plans/today/items/"+createdPlan.Items[0].ID+"/complete", tokens.AccessToken, nil)
	if completedPlanItem.Code != http.StatusNoContent {
		t.Fatalf("complete plan item=%d %s", completedPlanItem.Code, completedPlanItem.Body.String())
	}
	todayPlan := performJSON(t, server, http.MethodGet, "/v1/plans/today?date=2099-07-04&timezone=Asia%2FShanghai", tokens.AccessToken, nil)
	if todayPlan.Code != http.StatusOK || !strings.Contains(todayPlan.Body.String(), `"local_date":"2099-07-04"`) || !strings.Contains(todayPlan.Body.String(), `"status":"completed"`) {
		t.Fatalf("today plan=%d %s", todayPlan.Code, todayPlan.Body.String())
	}
	firstWebItem := performJSON(t, server, http.MethodPost, "/v1/plans/today/items", tokens.AccessToken, map[string]any{
		"title": "整理合同", "local_date": "2099-07-05", "timezone": "Asia/Shanghai", "source": "web",
	})
	secondWebItem := performJSON(t, server, http.MethodPost, "/v1/plans/today/items", tokens.AccessToken, map[string]any{
		"title": "提交周报", "local_date": "2099-07-05", "timezone": "Asia/Shanghai", "source": "web",
	})
	var firstAdded, secondAdded struct {
		ID string `json:"id"`
	}
	firstDecodeErr := json.Unmarshal(firstWebItem.Body.Bytes(), &firstAdded)
	secondDecodeErr := json.Unmarshal(secondWebItem.Body.Bytes(), &secondAdded)
	if firstWebItem.Code != http.StatusCreated || secondWebItem.Code != http.StatusCreated ||
		firstDecodeErr != nil || secondDecodeErr != nil || firstAdded.ID == "" || firstAdded.ID == secondAdded.ID {
		t.Fatalf("web items were not independently added: first=%d %s second=%d %s", firstWebItem.Code, firstWebItem.Body.String(), secondWebItem.Code, secondWebItem.Body.String())
	}
	webToday := performJSON(t, server, http.MethodGet, "/v1/plans/today?date=2099-07-05&timezone=Asia%2FShanghai", tokens.AccessToken, nil)
	if webToday.Code != http.StatusOK || !strings.Contains(webToday.Body.String(), `"title":"整理合同"`) || !strings.Contains(webToday.Body.String(), `"title":"提交周报"`) {
		t.Fatalf("web today plan=%d %s", webToday.Code, webToday.Body.String())
	}
	character := createCharacter(t, server, tokens.AccessToken, map[string]any{"name": "小棉", "personality": "温柔", "speech_style": "短句"})
	convResponse := performJSON(t, server, http.MethodPost, "/v1/conversations", tokens.AccessToken, map[string]string{"character_id": character.Character.ID})
	var conv struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(convResponse.Body.Bytes(), &conv)
	chat := performJSON(t, server, http.MethodPost, "/v1/conversations/"+conv.ID+"/messages", tokens.AccessToken, map[string]string{"content": "明早九点提醒我交报告"})
	if chat.Code != http.StatusAccepted || strings.Contains(chat.Body.String(), `"reminder_candidate"`) {
		t.Fatalf("chat must defer reminder intent to the model worker=%d %s", chat.Code, chat.Body.String())
	}
	candidateResponse := performJSON(t, server, http.MethodPost, "/v1/reminders/candidates", tokens.AccessToken, map[string]string{"text": "明早九点提醒我交报告"})
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("load timezone: %v", err)
	}
	expectedDue := time.Now().In(location).AddDate(0, 0, 1).Format("2006-01-02") + " 09:00"
	if candidateResponse.Code != http.StatusCreated || !strings.Contains(candidateResponse.Body.String(), `"local_due":"`+expectedDue+`"`) {
		t.Fatalf("candidate=%d %s", candidateResponse.Code, candidateResponse.Body.String())
	}
	var accepted struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(candidateResponse.Body.Bytes(), &accepted)
	confirmed := performReminderConfirm(t, server, tokens.AccessToken, accepted.ID, "confirm-report")
	if confirmed.Code != http.StatusCreated || !strings.Contains(confirmed.Body.String(), `"reminder":{"id"`) || !strings.Contains(confirmed.Body.String(), `"status":"active"`) || !strings.Contains(confirmed.Body.String(), `"deduplicated":false`) {
		t.Fatalf("confirm=%d %s", confirmed.Code, confirmed.Body.String())
	}
	var confirmedReminder struct {
		Reminder struct {
			UpdatedAt time.Time `json:"updated_at"`
		} `json:"reminder"`
	}
	if err := json.Unmarshal(confirmed.Body.Bytes(), &confirmedReminder); err != nil {
		t.Fatal(err)
	}
	rescheduled := performJSON(t, server, http.MethodPost, "/v1/reminders/"+accepted.ID+"/reschedule", tokens.AccessToken, map[string]any{"local_due": "2099-07-05 14:30", "timezone": "Asia/Shanghai", "expected_updated_at": confirmedReminder.Reminder.UpdatedAt})
	if rescheduled.Code != http.StatusOK || !strings.Contains(rescheduled.Body.String(), `"local_due":"2099-07-05 14:30"`) || !strings.Contains(rescheduled.Body.String(), `"time_precision":"minute"`) {
		t.Fatalf("reschedule reminder=%d %s", rescheduled.Code, rescheduled.Body.String())
	}
	updateChat := performJSON(t, server, http.MethodPost, "/v1/conversations/"+conv.ID+"/messages", tokens.AccessToken, map[string]string{"content": "把交报告提醒改到明天下午4点"})
	if updateChat.Code != http.StatusAccepted || strings.Contains(updateChat.Body.String(), `"reminder_candidate"`) {
		t.Fatalf("reminder update must not create a new reminder candidate: %d %s", updateChat.Code, updateChat.Body.String())
	}
	deduplicated := performReminderConfirm(t, server, tokens.AccessToken, accepted.ID, "confirm-report")
	if deduplicated.Code != http.StatusOK || !strings.Contains(deduplicated.Body.String(), `"deduplicated":true`) {
		t.Fatalf("deduplicated confirm=%d %s", deduplicated.Code, deduplicated.Body.String())
	}
	denied := performJSON(t, server, http.MethodPost, "/v1/reminders/"+accepted.ID+"/system-sync", tokens.AccessToken, map[string]string{"status": "permission_denied", "provider": "ios_eventkit", "error_code": "permission_denied"})
	if denied.Code != http.StatusOK || !strings.Contains(denied.Body.String(), `"system_sync_status":"permission_denied"`) || !strings.Contains(denied.Body.String(), `"status":"active"`) {
		t.Fatalf("denied=%d %s", denied.Code, denied.Body.String())
	}
	listed := performJSON(t, server, http.MethodGet, "/v1/reminders", tokens.AccessToken, nil)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), accepted.ID) {
		t.Fatalf("list=%d %s", listed.Code, listed.Body.String())
	}
	completed := performJSON(t, server, http.MethodPost, "/v1/reminders/"+accepted.ID+"/complete", tokens.AccessToken, nil)
	if completed.Code != http.StatusNoContent {
		t.Fatalf("complete=%d %s", completed.Code, completed.Body.String())
	}
	ambiguous := performJSON(t, server, http.MethodPost, "/v1/reminders/candidates", tokens.AccessToken, map[string]string{"text": "提醒我交报告"})
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
