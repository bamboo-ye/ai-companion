package httpserver

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/windcry1/ai-companion/internal/platform/config"
	"github.com/windcry1/ai-companion/internal/reliability"
)

func TestHealthAndReadiness(t *testing.T) {
	server := New(config.Config{HTTPAddr: ":0", ServiceName: "test", Environment: "test"}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	for _, path := range []string{"/healthz", "/readyz", "/v1/meta"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		server.httpServer.Handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, body = %s", path, response.Code, response.Body.String())
		}
		if traceID := response.Header().Get("X-Trace-ID"); traceID == "" {
			t.Fatalf("GET %s did not return a trace id", path)
		}
	}
	metricsRequest := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsResponse := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(metricsResponse, metricsRequest)
	if metricsResponse.Code != http.StatusOK || !strings.Contains(metricsResponse.Body.String(), `ai_companion_http_requests_total{method="GET",route="GET /healthz",status="200"} 1`) || !strings.Contains(metricsResponse.Body.String(), "ai_companion_degradation_level 0") {
		t.Fatalf("metrics response = %d %s", metricsResponse.Code, metricsResponse.Body.String())
	}
}

func TestSecurityHeaders(t *testing.T) {
	server := New(config.Config{HTTPAddr: ":0", ServiceName: "test", Environment: "test"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()

	server.httpServer.Handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	for header, want := range map[string]string{
		"X-Content-Type-Options":     "nosniff",
		"X-Frame-Options":            "DENY",
		"Referrer-Policy":            "no-referrer",
		"Cross-Origin-Opener-Policy": "same-origin",
	} {
		if got := response.Header().Get(header); got != want {
			t.Fatalf("%s = %q, want %q", header, got, want)
		}
	}
	if got := response.Header().Get("Permissions-Policy"); !strings.Contains(got, "camera=()") || !strings.Contains(got, "microphone=()") {
		t.Fatalf("Permissions-Policy = %q", got)
	}
}

func TestTracePropagationAndReliabilityEndpointAuthentication(t *testing.T) {
	server := New(config.Config{HTTPAddr: ":0", ServiceName: "test", Environment: "test", AuthTokenSecret: "reliability-test-secret-with-enough-entropy"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set("X-Trace-ID", "client_trace_1234567890")
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	if got := response.Header().Get("X-Trace-ID"); got != "client_trace_1234567890" {
		t.Fatalf("trace id = %q", got)
	}
	unauthorized := performJSON(t, server, http.MethodGet, "/v1/reliability", "", nil)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized reliability = %d %s", unauthorized.Code, unauthorized.Body.String())
	}
	register := performJSON(t, server, http.MethodPost, "/v1/auth/register", "", map[string]any{
		"email": "reliability@example.com", "password": "correct-horse-battery", "display_name": "Reliability", "timezone": "Asia/Shanghai",
		"device": map[string]any{"device_key": "reliability-web", "name": "Reliability Web", "platform": "web"},
	})
	var tokens struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(register.Body.Bytes(), &tokens); err != nil {
		t.Fatal(err)
	}
	reliability := performJSON(t, server, http.MethodGet, "/v1/reliability", tokens.AccessToken, nil)
	if reliability.Code != http.StatusOK || !strings.Contains(reliability.Body.String(), `"level":"L0"`) || !strings.Contains(reliability.Body.String(), `"preferred_model_class":"primary"`) {
		t.Fatalf("reliability = %d %s", reliability.Code, reliability.Body.String())
	}
}

func TestDocumentQuerySkipsRAGUnderDegradedPolicy(t *testing.T) {
	server := New(config.Config{HTTPAddr: ":0", ServiceName: "test", Environment: "test", AuthTokenSecret: "rag-degrade-secret-with-enough-entropy"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		server.ObserveReliability(reliability.Sample{QueueLag: 120}, now.Add(time.Duration(i)*time.Second))
	}
	register := performJSON(t, server, http.MethodPost, "/v1/auth/register", "", map[string]any{
		"email": "rag-degrade@example.com", "password": "correct-horse-battery", "display_name": "RAG", "timezone": "Asia/Shanghai",
		"device": map[string]any{"device_key": "rag", "name": "RAG", "platform": "web"},
	})
	var tokens struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.Unmarshal(register.Body.Bytes(), &tokens)
	query := performJSON(t, server, http.MethodPost, "/v1/documents/query", tokens.AccessToken, map[string]any{"query": "这份文档说了什么？"})
	if query.Code != http.StatusOK || !strings.Contains(query.Body.String(), `"degraded":true`) || !strings.Contains(query.Body.String(), `"sufficient":false`) {
		t.Fatalf("degraded query = %d %s", query.Code, query.Body.String())
	}
}

func TestCORSAllowsMemoryPatch(t *testing.T) {
	const origin = "http://localhost:3000"
	server := New(config.Config{HTTPAddr: ":0", ServiceName: "test", Environment: "test", WebOrigin: origin}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	request := httptest.NewRequest(http.MethodOptions, "/v1/memories/memory-1", nil)
	request.Header.Set("Origin", origin)
	request.Header.Set("Access-Control-Request-Method", http.MethodPatch)
	request.Header.Set("Access-Control-Request-Headers", "Idempotency-Key")
	response := httptest.NewRecorder()

	server.httpServer.Handler.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS status = %d, want %d", response.Code, http.StatusNoContent)
	}
	if methods := response.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(methods, http.MethodPatch) {
		t.Fatalf("allowed methods = %q, want PATCH", methods)
	}
	if headers := response.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(headers, "Idempotency-Key") {
		t.Fatalf("allowed headers = %q, want Idempotency-Key", headers)
	}
}

func TestDocumentUploadDeduplicateOwnershipAndDelete(t *testing.T) {
	server := New(config.Config{HTTPAddr: ":0", ServiceName: "test", Environment: "test", AuthTokenSecret: "a-test-secret-with-enough-entropy", DocumentMaxUploadBytes: 1024}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	register := performJSON(t, server, http.MethodPost, "/v1/auth/register", "", map[string]any{
		"email": "document-owner@example.com", "password": "correct-horse-battery", "display_name": "文档用户", "timezone": "Asia/Shanghai",
		"device": map[string]any{"device_key": "document-browser", "name": "Document Browser", "platform": "web"},
	})
	var ownerTokens struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(register.Body.Bytes(), &ownerTokens); err != nil {
		t.Fatal(err)
	}
	first := performUpload(t, server, ownerTokens.AccessToken, "knowledge.txt", []byte("第一章\n文档证据必须带出处。"))
	if first.Code != http.StatusAccepted {
		t.Fatalf("upload status = %d, body = %s", first.Code, first.Body.String())
	}
	var uploaded struct {
		Document struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"document"`
		Deduplicated bool `json:"deduplicated"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &uploaded); err != nil {
		t.Fatal(err)
	}
	if uploaded.Document.ID == "" || uploaded.Document.Status != "queued" || uploaded.Deduplicated {
		t.Fatalf("upload response = %#v", uploaded)
	}
	duplicate := performUpload(t, server, ownerTokens.AccessToken, "renamed.txt", []byte("第一章\n文档证据必须带出处。"))
	if duplicate.Code != http.StatusOK || !strings.Contains(duplicate.Body.String(), uploaded.Document.ID) || !strings.Contains(duplicate.Body.String(), `"deduplicated":true`) {
		t.Fatalf("duplicate response = %d %s", duplicate.Code, duplicate.Body.String())
	}
	query := performJSON(t, server, http.MethodPost, "/v1/documents/query", ownerTokens.AccessToken, map[string]any{"query": "文档证据是什么？"})
	if query.Code != http.StatusOK || !strings.Contains(query.Body.String(), `"sufficient":false`) || !strings.Contains(query.Body.String(), `"citations":[]`) {
		t.Fatalf("queued document query = %d %s", query.Code, query.Body.String())
	}

	otherRegister := performJSON(t, server, http.MethodPost, "/v1/auth/register", "", map[string]any{
		"email": "document-other@example.com", "password": "correct-horse-battery", "display_name": "其他用户", "timezone": "Asia/Shanghai",
		"device": map[string]any{"device_key": "other-browser", "name": "Other Browser", "platform": "web"},
	})
	var otherTokens struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(otherRegister.Body.Bytes(), &otherTokens); err != nil {
		t.Fatal(err)
	}
	otherGet := performJSON(t, server, http.MethodGet, "/v1/documents/"+uploaded.Document.ID, otherTokens.AccessToken, nil)
	if otherGet.Code != http.StatusNotFound {
		t.Fatalf("cross-user get = %d %s", otherGet.Code, otherGet.Body.String())
	}
	deleted := performJSON(t, server, http.MethodDelete, "/v1/documents/"+uploaded.Document.ID, ownerTokens.AccessToken, nil)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", deleted.Code, deleted.Body.String())
	}
	missing := performJSON(t, server, http.MethodGet, "/v1/documents/"+uploaded.Document.ID, ownerTokens.AccessToken, nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("deleted get = %d %s", missing.Code, missing.Body.String())
	}
}

func TestAuthCharacterAndPersonaVersionFlow(t *testing.T) {
	server := New(config.Config{HTTPAddr: ":0", ServiceName: "test", Environment: "test", AuthTokenSecret: "a-test-secret-with-enough-entropy", ChatRateLimit: 1, ChatRateWindow: time.Minute}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	register := performJSON(t, server, http.MethodPost, "/v1/auth/register", "", map[string]any{
		"email": "lin@example.com", "password": "correct-horse-battery", "display_name": "小林", "timezone": "Asia/Shanghai",
		"device": map[string]any{"device_key": "test-browser", "name": "Test Browser", "platform": "web"},
	})
	if register.Code != http.StatusCreated {
		t.Fatalf("register status = %d, body = %s", register.Code, register.Body.String())
	}
	var tokens struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(register.Body.Bytes(), &tokens); err != nil {
		t.Fatal(err)
	}
	if tokens.AccessToken == "" || tokens.RefreshToken == "" {
		t.Fatal("registration did not return tokens")
	}

	gentle := createCharacter(t, server, tokens.AccessToken, map[string]any{
		"name": "小棉", "relationship": "温柔朋友", "personality": "耐心、体贴，不说教", "speech_style": "短句，先共情再回应", "hobbies": []string{"电影"}, "initiative": "balanced", "reply_length": "short",
	})
	direct := createCharacter(t, server, tokens.AccessToken, map[string]any{
		"name": "阿策", "relationship": "行动教练", "personality": "直接、清醒、重视行动", "speech_style": "先给结论，再给三步建议", "hobbies": []string{"效率工具"}, "initiative": "high", "reply_length": "medium",
	})
	if gentle.Persona.Compiled["voice"] == nil || direct.Persona.Compiled["voice"] == nil {
		t.Fatal("compiled personas missing voice")
	}
	gentleVoice, _ := json.Marshal(gentle.Persona.Compiled["voice"])
	directVoice, _ := json.Marshal(direct.Persona.Compiled["voice"])
	if bytes.Equal(gentleVoice, directVoice) {
		t.Fatal("different character inputs compiled to the same voice")
	}

	list := performJSON(t, server, http.MethodGet, "/v1/characters", tokens.AccessToken, nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", list.Code, list.Body.String())
	}
	var listed struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Items) != 2 {
		t.Fatalf("character count = %d, want 2", len(listed.Items))
	}

	updated := performJSON(t, server, http.MethodPut, "/v1/characters/"+gentle.Character.ID, tokens.AccessToken, map[string]any{
		"name": "小棉", "relationship": "温柔朋友", "personality": "耐心、体贴，也会温和提醒", "speech_style": "短句，先共情再回应", "initiative": "balanced", "reply_length": "short",
	})
	if updated.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", updated.Code, updated.Body.String())
	}
	versions := performJSON(t, server, http.MethodGet, "/v1/characters/"+gentle.Character.ID+"/persona-versions", tokens.AccessToken, nil)
	var history struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(versions.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if len(history.Items) != 2 {
		t.Fatalf("persona version count = %d, want 2", len(history.Items))
	}

	createdConversation := performJSON(t, server, http.MethodPost, "/v1/conversations", tokens.AccessToken, map[string]string{"character_id": gentle.Character.ID})
	if createdConversation.Code != http.StatusCreated {
		t.Fatalf("create conversation status = %d, body = %s", createdConversation.Code, createdConversation.Body.String())
	}
	var conversation struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(createdConversation.Body.Bytes(), &conversation); err != nil {
		t.Fatal(err)
	}
	accepted := performJSON(t, server, http.MethodPost, "/v1/conversations/"+conversation.ID+"/messages", tokens.AccessToken, map[string]string{"content": "今天有点累，也担心明天的汇报。"})
	if accepted.Code != http.StatusAccepted {
		t.Fatalf("send status = %d, body = %s", accepted.Code, accepted.Body.String())
	}
	rateLimited := performJSON(t, server, http.MethodPost, "/v1/conversations/"+conversation.ID+"/messages", tokens.AccessToken, map[string]string{"content": "第二条太快的消息"})
	if rateLimited.Code != http.StatusTooManyRequests {
		t.Fatalf("second message status = %d, want 429", rateLimited.Code)
	}
	presence := performJSON(t, server, http.MethodGet, "/v1/presence/me", tokens.AccessToken, nil)
	if presence.Code != http.StatusOK || !strings.Contains(presence.Body.String(), "true") {
		t.Fatalf("presence response = %d %s", presence.Code, presence.Body.String())
	}
	var generation struct {
		Job struct {
			ID string `json:"id"`
		} `json:"job"`
	}
	if err := json.Unmarshal(accepted.Body.Bytes(), &generation); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	status := ""
	for time.Now().Before(deadline) {
		response := performJSON(t, server, http.MethodGet, "/v1/generation-jobs/"+generation.Job.ID, tokens.AccessToken, nil)
		var job struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(response.Body.Bytes(), &job)
		status = job.Status
		if status == "completed" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if status != "completed" {
		t.Fatalf("generation status = %s, want completed", status)
	}
	messages := performJSON(t, server, http.MethodGet, "/v1/conversations/"+conversation.ID+"/messages", tokens.AccessToken, nil)
	var messagePage struct {
		Items []struct {
			Role     string `json:"role"`
			Sequence uint64 `json:"sequence"`
			Bubble   int    `json:"bubble"`
		} `json:"items"`
	}
	if err := json.Unmarshal(messages.Body.Bytes(), &messagePage); err != nil {
		t.Fatal(err)
	}
	if len(messagePage.Items) < 3 || len(messagePage.Items) > 6 {
		t.Fatalf("message bubble count = %d, want user + 2-5 assistant bubbles", len(messagePage.Items))
	}
	if messagePage.Items[0].Sequence != 1 || messagePage.Items[1].Sequence != 2 {
		t.Fatalf("unexpected sequence order: %#v", messagePage.Items)
	}
	remaining := performJSON(t, server, http.MethodGet, "/v1/conversations/"+conversation.ID+"/messages?after_sequence=2&after_bubble=2", tokens.AccessToken, nil)
	var remainingPage struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(remaining.Body.Bytes(), &remainingPage); err != nil {
		t.Fatal(err)
	}
	expectedRemaining := len(messagePage.Items) - 3
	if len(remainingPage.Items) != expectedRemaining {
		t.Fatalf("bubble cursor returned %d items, want %d", len(remainingPage.Items), expectedRemaining)
	}
	stream := performJSON(t, server, http.MethodGet, "/v1/generation-jobs/"+generation.Job.ID+"/events", tokens.AccessToken, nil)
	if stream.Code != http.StatusOK || !strings.Contains(stream.Body.String(), "event: completed") {
		t.Fatalf("SSE replay missing completion: %s", stream.Body.String())
	}

	refresh := performJSON(t, server, http.MethodPost, "/v1/auth/refresh", "", map[string]string{"refresh_token": tokens.RefreshToken})
	if refresh.Code != http.StatusOK {
		t.Fatalf("refresh status = %d, body = %s", refresh.Code, refresh.Body.String())
	}
	reused := performJSON(t, server, http.MethodPost, "/v1/auth/refresh", "", map[string]string{"refresh_token": tokens.RefreshToken})
	if reused.Code != http.StatusUnauthorized {
		t.Fatalf("reused refresh status = %d, want 401", reused.Code)
	}
}

type characterResponse struct {
	Character struct {
		ID string `json:"id"`
	} `json:"character"`
	Persona struct {
		Compiled map[string]any `json:"compiled"`
	} `json:"persona"`
}

func createCharacter(t *testing.T, server *Server, token string, body map[string]any) characterResponse {
	t.Helper()
	response := performJSON(t, server, http.MethodPost, "/v1/characters", token, body)
	if response.Code != http.StatusCreated {
		t.Fatalf("create character status = %d, body = %s", response.Code, response.Body.String())
	}
	var result characterResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func performJSON(t *testing.T, server *Server, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		payload = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, path, payload)
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	return response
}

func performUpload(t *testing.T, server *Server, token, name string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/documents", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	return response
}
