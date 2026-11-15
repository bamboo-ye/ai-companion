package httpserver

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/windcry1/ai-companion/internal/platform/config"
)

func TestHealthAssistantGatewayRequiresActiveSessionAndStampsOwner(t *testing.T) {
	const serviceToken = "health-gateway-test-token-at-least-32"
	type sentRequest struct {
		path, authorization, owner, timezone, cookie, query, lastEvent string
	}
	var callCount atomic.Int32
	requests := make(chan sentRequest, 3)
	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		requests <- sentRequest{r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("X-Companion-User-ID"), r.Header.Get("X-Companion-Timezone"), r.Header.Get("Cookie"), r.URL.Query().Get("after"), r.Header.Get("Last-Event-ID")}
		w.Header().Set("Set-Cookie", "upstream=data")
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "public")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, "event: end\ndata: {\"status\":\"succeeded\"}\n\n")
	}))
	defer private.Close()
	server := New(config.Config{
		HealthAssistantURL: private.URL, HealthAssistantToken: serviceToken,
		AuthTokenSecret: "health-test-auth-token-secret", WebOrigin: "http://localhost:3000",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	user := registerProfileUser(t, server, "health-test-device", "health-test-password")
	profile := performJSON(t, server, http.MethodGet, "/v1/users/me", user.AccessToken, nil)
	var owner struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(profile.Body.Bytes(), &owner); err != nil || owner.ID == "" {
		t.Fatalf("could not load test account ID: %v", err)
	}

	const route = "/v1/health/runs/test-id/events"
	for _, token := range []string{"", serviceToken} {
		code := performJSON(t, server, http.MethodGet, route, token, nil).Code
		if code != http.StatusUnauthorized {
			t.Fatalf("request without a valid user session: got %d", code)
		}
	}

	request := httptest.NewRequest(http.MethodGet, route+"?after=11", nil)
	request.Header.Set("Authorization", "Bearer "+user.AccessToken)
	request.Header.Set("Cookie", "unrelated-cookie=secret")
	request.Header.Set("X-Companion-User-ID", "not-the-caller")
	request.Header.Set("X-Companion-Timezone", "UTC")
	request.Header.Set("Last-Event-ID", "11")
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	if response.Code != 200 || !response.Flushed || !strings.Contains(response.Body.String(), "event: end") ||
		response.Header().Get("Set-Cookie") != "" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected stream response: %d %s", response.Code, response.Body.String())
	}
	got := <-requests
	if got != (sentRequest{route, "Bearer " + serviceToken, owner.ID, "Asia/Shanghai", "", "11", "11"}) {
		t.Fatalf("unexpected request forwarded to private health service")
	}
	if code := performJSON(t, server, http.MethodGet, "/v1/health/research/records", user.AccessToken, nil).Code; code != http.StatusNotFound {
		t.Fatalf("non-personal route = %d", code)
	}

	performJSON(t, server, http.MethodPost, "/v1/auth/logout", user.AccessToken, nil)
	if code := performJSON(t, server, http.MethodGet, route, user.AccessToken, nil).Code; code != http.StatusUnauthorized {
		t.Fatalf("revoked session received %d", code)
	}
	if callCount.Load() != 1 {
		t.Fatalf("private health service received %d requests", callCount.Load())
	}
}

func TestOptionalHealthAssistantReportsDisabled(t *testing.T) {
	server := New(config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	user := registerProfileUser(t, server, "disabled-health-test", "health-test-password")
	result := performJSON(t, server, http.MethodPost, "/v1/health/connect", user.AccessToken, map[string]any{})
	if result.Code != http.StatusServiceUnavailable || !strings.Contains(result.Body.String(), "health_disabled") {
		t.Fatalf("unconfigured health assistant = %d %s", result.Code, result.Body.String())
	}
}
