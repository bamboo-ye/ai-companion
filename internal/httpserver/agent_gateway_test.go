package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/windcry1/ai-companion/internal/agent"
	"github.com/windcry1/ai-companion/internal/conversation"
	"github.com/windcry1/ai-companion/internal/ledger"
	"github.com/windcry1/ai-companion/internal/platform/config"
)

type agentHTTPStore struct {
	run agent.Run
}

func (s *agentHTTPStore) CreateAgentRun(_ context.Context, item agent.Run) (agent.Run, bool, error) {
	s.run = item
	return item, true, nil
}

func (s *agentHTTPStore) GetAgentRun(_ context.Context, runID string) (agent.Run, error) {
	if s.run.ID != runID {
		return agent.Run{}, agent.ErrNotFound
	}
	return s.run, nil
}

func (s *agentHTTPStore) GetActiveAgentRun(_ context.Context, userID, conversationID string) (agent.Run, error) {
	if s.run.UserID != userID || s.run.ConversationID != conversationID || agent.IsTerminalStatus(s.run.Status) {
		return agent.Run{}, agent.ErrNotFound
	}
	return s.run, nil
}

func (s *agentHTTPStore) ClaimAgentRun(context.Context, string, string, time.Time, time.Duration) (agent.Run, error) {
	return agent.Run{}, agent.ErrConflict
}

func (s *agentHTTPStore) ClaimNextAgentRun(context.Context, string, time.Time, time.Duration) (agent.Run, error) {
	return agent.Run{}, agent.ErrNotFound
}

func (s *agentHTTPStore) DeferAgentRun(context.Context, string, string, int, time.Time, string, time.Time) (agent.Run, error) {
	return agent.Run{}, agent.ErrConflict
}

func (s *agentHTTPStore) PauseAgentRun(context.Context, string, string, int, json.RawMessage, time.Time) (agent.Run, error) {
	return agent.Run{}, agent.ErrConflict
}

func (s *agentHTTPStore) SuspendAgentRunForTool(context.Context, string, string, int, json.RawMessage, time.Time, time.Time) (agent.Run, error) {
	return agent.Run{}, agent.ErrConflict
}

func (s *agentHTTPStore) WakeAgentRunsForTool(context.Context, string, string, time.Time, int) ([]agent.Run, error) {
	return nil, nil
}

func (s *agentHTTPStore) ResolveAgentRun(context.Context, string, string, bool, string, time.Time) (agent.Run, bool, error) {
	s.run.Status = "queued"
	s.run.Resume = json.RawMessage(`{"approved":true}`)
	s.run.Revision++
	return s.run, true, nil
}

func (s *agentHTTPStore) CancelAgentRun(_ context.Context, runID, userID string, _ time.Time) (agent.Run, bool, error) {
	if s.run.ID != runID || s.run.UserID != userID {
		return agent.Run{}, false, agent.ErrNotFound
	}
	if agent.IsTerminalStatus(s.run.Status) || s.run.Status == "cancel_requested" {
		return s.run, false, nil
	}
	if s.run.Status == "running" {
		s.run.Status = "cancel_requested"
	} else {
		s.run.Status = "cancelled"
	}
	s.run.Revision++
	return s.run, true, nil
}

func (s *agentHTTPStore) FinalizeAgentRunCancellation(context.Context, string, string, int, time.Time) (agent.Run, error) {
	return agent.Run{}, agent.ErrConflict
}

func (s *agentHTTPStore) TimeoutAgentRun(context.Context, string, string, int, string, string, time.Time) (agent.Run, error) {
	return agent.Run{}, agent.ErrConflict
}

func (s *agentHTTPStore) ExpireAgentRuns(context.Context, time.Time, int) (int, error) {
	return 0, nil
}

func (s *agentHTTPStore) AcceptAgentMessage(_ context.Context, message conversation.Message, run agent.Run) (conversation.Message, agent.Run, error) {
	message.Sequence = 1
	s.run = run
	return message, run, nil
}

func (s *agentHTTPStore) CompleteAgentRun(context.Context, string, string, int, json.RawMessage, time.Time) (agent.Run, error) {
	return agent.Run{}, agent.ErrConflict
}

func (s *agentHTTPStore) FailAgentRun(context.Context, string, string, int, string, string, time.Time) (agent.Run, error) {
	return agent.Run{}, agent.ErrConflict
}

func (s *agentHTTPStore) ListAgentRunEvents(context.Context, string, int64, int) ([]agent.Event, error) {
	return nil, nil
}

func TestAgentGatewayHTTPRequiresServiceTokenAndCommits(t *testing.T) {
	const token = "agent-gateway-http-token"
	server := New(config.Config{
		HTTPAddr: ":0", ServiceName: "test", Environment: "test",
		AgentGatewayToken: token, AgentConfirmationSecret: "agent-http-confirmation-secret",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	path := "/internal/v1/agent/runs/run-http-1/tools/prepare"
	unauthorized := performJSON(t, server, http.MethodPost, path, "", map[string]any{
		"call_key": "prepare", "tool_name": "life_prepare_ledger_entry", "arguments": map[string]any{},
	})
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, body = %s", unauthorized.Code, unauthorized.Body.String())
	}
	unavailable := performJSON(t, server, http.MethodPost, path, token, map[string]any{
		"call_key": "prepare", "tool_name": "life_prepare_ledger_entry", "arguments": map[string]any{},
	})
	if unavailable.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable status = %d, body = %s", unavailable.Code, unavailable.Body.String())
	}

	runInput, err := json.Marshal(map[string]any{
		"message_id": "message-http-1", "text": "我今天吃饭花了50元",
	})
	if err != nil {
		t.Fatal(err)
	}
	leaseExpiresAt := time.Now().UTC().Add(time.Hour)
	server.SetAgentStore(&agentHTTPStore{run: agent.Run{
		ID: "run-http-1", ThreadID: "run-http-1", UserID: "user-http-1",
		ConversationID: "conversation-http-1", CharacterID: "character-http-1",
		Module: "life", Status: "running", Input: runInput, Revision: 1,
		LeaseOwner: "worker-http", LeaseExpiresAt: &leaseExpiresAt, DeadlineAt: leaseExpiresAt,
	}})
	preparedResponse := performAgentJSON(t, server, http.MethodPost, path, token, 1, map[string]any{
		"call_key": "prepare", "tool_name": "life_prepare_ledger_entry", "arguments": map[string]any{},
	})
	if preparedResponse.Code != http.StatusOK {
		t.Fatalf("prepare status = %d, body = %s", preparedResponse.Code, preparedResponse.Body.String())
	}
	var prepared agent.ToolPreparation
	if err = json.Unmarshal(preparedResponse.Body.Bytes(), &prepared); err != nil {
		t.Fatal(err)
	}
	if prepared.Status != "requires_confirmation" || prepared.ConfirmationToken == "" {
		t.Fatalf("prepared = %#v", prepared)
	}

	committed := performAgentJSON(t, server, http.MethodPost, "/internal/v1/agent/runs/run-http-1/tools/commit", token, 1, map[string]any{
		"call_key": "commit", "confirmation_token": prepared.ConfirmationToken, "approved": true,
	})
	if committed.Code != http.StatusOK {
		t.Fatalf("commit status = %d, body = %s", committed.Code, committed.Body.String())
	}
	entries, err := server.ledger.List(context.Background(), "user-http-1", ledger.EntryFilter{Limit: 20})
	if err != nil || len(entries) != 1 || entries[0].AmountMinor != 5000 {
		t.Fatalf("entries = %#v, %v", entries, err)
	}
}

func performAgentJSON(t *testing.T, server *Server, method, path, token string, revision int, body any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, path, strings.NewReader(string(encoded)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Agent-Run-Revision", fmt.Sprintf("%d", revision))
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	return response
}

func TestAgentChatFeatureFlagCreatesOnlyAgentRunAndResolvesApproval(t *testing.T) {
	server := New(config.Config{
		HTTPAddr: ":0", ServiceName: "test", Environment: "test",
		AuthTokenSecret:  "agent-chat-http-secret-with-enough-entropy",
		AgentChatModules: []string{"life"},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	store := &agentHTTPStore{}
	server.SetAgentStore(store)
	register := performJSON(t, server, http.MethodPost, "/v1/auth/register", "", map[string]any{
		"email": "agent-chat@example.com", "password": "correct-horse-battery",
		"display_name": "Agent Chat", "timezone": "Asia/Shanghai",
		"device": map[string]any{"device_key": "agent-chat", "name": "Agent Chat", "platform": "web"},
	})
	var tokens struct {
		AccessToken string `json:"access_token"`
		User        struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal(register.Body.Bytes(), &tokens); err != nil {
		t.Fatal(err)
	}
	character := createCharacter(t, server, tokens.AccessToken, map[string]any{
		"module": "life", "name": "小满", "personality": "细心", "speech_style": "简洁",
	})
	createdConversation := performJSON(
		t, server, http.MethodPost, "/v1/conversations", tokens.AccessToken,
		map[string]string{"character_id": character.Character.ID},
	)
	var chat struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(createdConversation.Body.Bytes(), &chat); err != nil {
		t.Fatal(err)
	}
	accepted := performJSON(
		t, server, http.MethodPost, "/v1/conversations/"+chat.ID+"/messages",
		tokens.AccessToken, map[string]string{"content": "今天午饭花了50元"},
	)
	if accepted.Code != http.StatusAccepted ||
		!strings.Contains(accepted.Body.String(), `"runtime":"agent"`) ||
		!strings.Contains(accepted.Body.String(), `"agent_run"`) ||
		strings.Contains(accepted.Body.String(), `"job"`) {
		t.Fatalf("Agent chat response = %d %s", accepted.Code, accepted.Body.String())
	}
	var runInput struct {
		Context struct {
			EmailProfile struct {
				SenderName      string `json:"sender_name"`
				DefaultLanguage string `json:"default_language"`
				ProfileVersion  string `json:"profile_version"`
			} `json:"email_profile"`
		} `json:"context"`
	}
	if err := json.Unmarshal(store.run.Input, &runInput); err != nil {
		t.Fatal(err)
	}
	if runInput.Context.EmailProfile.SenderName != "Agent Chat" ||
		runInput.Context.EmailProfile.DefaultLanguage != "zh-CN" ||
		runInput.Context.EmailProfile.ProfileVersion != "account-email-profile-v1" {
		t.Fatalf("email profile context = %#v", runInput.Context.EmailProfile)
	}
	store.run.Status = "waiting_approval"
	store.run.Output = json.RawMessage(`{"interrupts":[{"type":"tool_approval","summary":"餐饮支出 50 元","confirmation_token":"signed"}]}`)
	current := performJSON(
		t, server, http.MethodGet, "/v1/agent-runs/"+store.run.ID,
		tokens.AccessToken, nil,
	)
	if current.Code != http.StatusOK || !strings.Contains(current.Body.String(), "餐饮支出 50 元") {
		t.Fatalf("GET Agent Run = %d %s", current.Code, current.Body.String())
	}
	if strings.Contains(current.Body.String(), "confirmation_token") ||
		strings.Contains(current.Body.String(), "signed") {
		t.Fatalf("GET Agent Run exposed confirmation token: %s", current.Body.String())
	}
	resolveRequest := performJSON(
		t, server, http.MethodPost, "/v1/agent-runs/"+store.run.ID+"/resolve",
		tokens.AccessToken, map[string]bool{"approved": true},
	)
	if resolveRequest.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing idempotency key = %d %s", resolveRequest.Code, resolveRequest.Body.String())
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/agent-runs/"+store.run.ID+"/resolve",
		strings.NewReader(`{"approved":true}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	request.Header.Set("Idempotency-Key", store.run.ID+":3")
	resolved := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(resolved, request)
	if resolved.Code != http.StatusAccepted || !strings.Contains(resolved.Body.String(), `"status":"queued"`) {
		t.Fatalf("resolve Agent Run = %d %s", resolved.Code, resolved.Body.String())
	}
	cancelRequest := httptest.NewRequest(
		http.MethodPost,
		"/v1/agent-runs/"+store.run.ID+"/cancel",
		nil,
	)
	cancelRequest.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	cancelRequest.Header.Set("Idempotency-Key", store.run.ID+":cancel")
	cancelled := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(cancelled, cancelRequest)
	if cancelled.Code != http.StatusAccepted ||
		!strings.Contains(cancelled.Body.String(), `"status":"cancelled"`) {
		t.Fatalf("cancel Agent Run = %d %s", cancelled.Code, cancelled.Body.String())
	}
	cancelledReplay := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(cancelledReplay, cancelRequest.Clone(context.Background()))
	if cancelledReplay.Code != http.StatusOK ||
		!strings.Contains(cancelledReplay.Body.String(), `"deduplicated":true`) {
		t.Fatalf("replay cancel Agent Run = %d %s", cancelledReplay.Code, cancelledReplay.Body.String())
	}
	retryRequest := httptest.NewRequest(
		http.MethodPost,
		"/v1/agent-runs/"+store.run.ID+"/retry",
		nil,
	)
	retryRequest.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	retryRequest.Header.Set("Idempotency-Key", store.run.ID+":retry")
	retried := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(retried, retryRequest)
	if retried.Code != http.StatusAccepted ||
		!strings.Contains(retried.Body.String(), `"status":"accepted"`) {
		t.Fatalf("retry Agent Run = %d %s", retried.Code, retried.Body.String())
	}
}
