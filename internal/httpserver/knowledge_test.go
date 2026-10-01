package httpserver

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/windcry1/ai-companion/internal/knowledge"
	"github.com/windcry1/ai-companion/internal/platform/config"
)

func TestKnowledgeHTTPPermissionsVersionsAndFeedback(t *testing.T) {
	s := New(config.Config{Environment: "test", AuthTokenSecret: "knowledge-http-test-secret", OperatorToken: "knowledge-operator"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	registered := performJSON(t, s, "POST", "/v1/auth/register", "", map[string]any{"email": "knowledge@example.com", "password": "test-password-long", "display_name": "knowledge", "timezone": "Asia/Shanghai", "device": map[string]any{"device_key": "knowledge", "name": "test", "platform": "web"}})
	var session struct {
		Token string `json:"access_token"`
		User  struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal(registered.Body.Bytes(), &session); err != nil || session.Token == "" {
		t.Fatal(registered.Body.String())
	}
	item, err := s.memories.Create(context.Background(), session.User.ID, "喜欢冰岛极光摄影")
	if err != nil {
		t.Fatal(err)
	}
	q := knowledge.Query{Query: "冰岛极光", Kinds: []string{"memory"}}
	if r := performJSON(t, s, "POST", "/v1/knowledge/search", "", q); r.Code != 401 {
		t.Fatal("anonymous allowed", r.Code)
	}
	r := performJSON(t, s, "POST", "/v1/knowledge/search", session.Token, q)
	var result knowledge.Result
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &result) != nil || len(result.Hits) != 1 {
		t.Fatal(r.Code, r.Body.String())
	}
	if r.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("private evidence can be cached")
	}
	ref := result.Hits[0].Ref
	if r = performJSON(t, s, "POST", "/v1/knowledge/validate", session.Token, map[string]any{"evidence": []knowledge.Evidence{{Ref: ref, Quote: "冰岛极光"}}}); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if r = performJSON(t, s, "POST", "/v1/knowledge/feedback", session.Token, knowledge.Feedback{Ref: ref, Rating: "incorrect", Note: "核对偏好"}); r.Code != 201 {
		t.Fatal(r.Body.String())
	}
	for _, path := range []string{"/v1/ops/knowledge/feedback", "/v1/ops/knowledge/metrics"} {
		if r = performJSON(t, s, "GET", path, session.Token, nil); r.Code == 200 {
			t.Fatal("user gained operator authority")
		}
	}
	q.WorkspaceID = "team"
	if r = performJSON(t, s, "POST", "/v1/knowledge/search", session.Token, q); r.Code != 403 {
		t.Fatal("personal memory entered workspace", r.Code)
	}
	q.Kinds = []string{"document"}
	if r = performJSON(t, s, "POST", "/v1/knowledge/search", session.Token, q); r.Code != 403 {
		t.Fatal("workspace membership bypass", r.Code)
	}
	if _, err = s.memories.Correct(context.Background(), session.User.ID, item.ID, "不再喜欢冰岛极光摄影"); err != nil {
		t.Fatal(err)
	}
	if r = performJSON(t, s, "POST", "/v1/knowledge/read", session.Token, knowledge.ReadInput{Ref: ref}); r.Code != 404 {
		t.Fatal("superseded memory readable", r.Code)
	}
}

func TestKnowledgeOperationsEnforceRoles(t *testing.T) {
	s := New(config.Config{Environment: "test"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, test := range []struct {
		role    string
		handler func(http.ResponseWriter, *http.Request)
	}{{"viewer", s.retryKnowledge}, {"viewer", s.pendingKnowledgeFeedback}, {"viewer", s.resolveKnowledgeFeedback}, {"support", s.rebuildKnowledge}} {
		req := httptest.NewRequest("POST", "/", nil)
		req = req.WithContext(context.WithValue(req.Context(), operatorContextKey{}, operatorAuth{Actor: "operator", Role: test.role}))
		w := httptest.NewRecorder()
		test.handler(w, req)
		if w.Code != 403 {
			t.Fatal("role escaped", test.role, w.Code)
		}
	}
}
