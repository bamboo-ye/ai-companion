package httpserver

import (
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/windcry1/ai-companion/internal/contextengine"
	"github.com/windcry1/ai-companion/internal/platform/config"
)

func TestBuiltinKnowledgeHTTPWorksWithoutUploadsAndIsReadOnly(t *testing.T) {
	server := New(config.Config{Environment: "test", AuthTokenSecret: "builtin-knowledge-test-secret"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if res := performJSON(t, server, "GET", "/v1/knowledge/builtin/pages", "", nil); res.Code != 401 {
		t.Fatalf("auth missing: %d", res.Code)
	}
	registered := performJSON(t, server, "POST", "/v1/auth/register", "", map[string]any{"email": "builtin@example.com", "password": "test-password-long", "display_name": "guide", "timezone": "Asia/Shanghai", "device": map[string]any{"device_key": "builtin", "name": "test", "platform": "web"}})
	var session struct {
		Token string `json:"access_token"`
	}
	if err := json.Unmarshal(registered.Body.Bytes(), &session); err != nil || session.Token == "" {
		t.Fatal(registered.Body.String())
	}
	res := performJSON(t, server, "GET", "/v1/knowledge/builtin/pages", session.Token, nil)
	if res.Code != 200 || !strings.Contains(res.Body.String(), "builtin-user-welcome") {
		t.Fatal(res.Body.String())
	}
	res = performJSON(t, server, "POST", "/v1/knowledge/builtin/search", session.Token, map[string]any{"query": "如何上传文档到知识库"})
	if res.Code != 200 || !strings.Contains(res.Body.String(), "builtin-user-documents") {
		t.Fatal(res.Body.String())
	}
	res = performJSON(t, server, "GET", "/v1/knowledge/builtin/pages/builtin-user-profile", session.Token, nil)
	if res.Code != 200 || !strings.Contains(res.Body.String(), "其他设备") || res.Header().Get("ETag") == "" {
		t.Fatal(res.Body.String())
	}
	for _, method := range []string{"PATCH", "DELETE", "POST"} {
		if res = performJSON(t, server, method, "/v1/knowledge/builtin/pages/builtin-user-profile", session.Token, map[string]any{"body": "changed"}); res.Code != 405 {
			t.Fatalf("read-only violation: %s %d", method, res.Code)
		}
	}
	if res = performJSON(t, server, "GET", "/v1/knowledge/builtin/pages/private-id", session.Token, nil); res.Code != 404 {
		t.Fatal("private page lookup accepted")
	}
	if res = performJSON(t, server, "POST", "/v1/knowledge/builtin/search", session.Token, map[string]any{"query": "a", "limit": 500}); res.Code != 422 {
		t.Fatal("unbounded search")
	}
	// Public explanatory admin guides never confer actual operator authority.
	if res = performJSON(t, server, "GET", "/v1/ops/context/metrics", session.Token, nil); res.Code == 200 {
		t.Fatal("help granted operator access")
	}
}

func TestBuiltinKnowledgeIsCapturedByAgentIntakeForEveryModule(t *testing.T) {
	for _, module := range []string{"companion", "life", "work"} {
		t.Run(module, func(t *testing.T) {
			server := New(config.Config{Environment: "test", AuthTokenSecret: "product-agent-test-secret", AgentChatModules: []string{module}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			store := &agentHTTPStore{}
			server.SetAgentStore(store)
			registered := performJSON(t, server, "POST", "/v1/auth/register", "", map[string]any{"email": module + "@example.com", "password": "test-password-long", "display_name": "guide", "timezone": "Asia/Shanghai", "device": map[string]any{"device_key": module, "name": "test", "platform": "web"}})
			var account struct {
				Token string `json:"access_token"`
				User  struct {
					ID string `json:"id"`
				} `json:"user"`
			}
			if err := json.Unmarshal(registered.Body.Bytes(), &account); err != nil || account.Token == "" {
				t.Fatal(registered.Body.String())
			}
			persona := createCharacter(t, server, account.Token, map[string]any{"module": module, "name": "帮助助手", "personality": "细心", "speech_style": "简洁"})
			chat, err := server.conversations.Create(t.Context(), account.User.ID, persona.Character.ID)
			if err != nil {
				t.Fatal(err)
			}
			response := performJSON(t, server, "POST", "/v1/conversations/"+chat.ID+"/messages", account.Token, map[string]string{"content": "如何修改登录密码"})
			if response.Code != 202 {
				t.Fatalf("send %d %s", response.Code, response.Body.String())
			}
			var input struct {
				Context struct {
					Snapshot contextengine.Snapshot `json:"conversation_context"`
				} `json:"context"`
			}
			if err = json.Unmarshal(store.run.Input, &input); err != nil {
				t.Fatal(err)
			}
			if len(input.Context.Snapshot.Knowledge) == 0 || !strings.Contains(input.Context.Snapshot.ReferenceText(), "当前密码") {
				t.Fatal("missing product knowledge in persisted agent input")
			}
		})
	}
}
