package httpserver

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/windcry1/ai-companion/internal/platform/config"
)

func TestIntentRouteAndMCPConfiguredCatalog(t *testing.T) {
	server := New(config.Config{
		HTTPAddr: ":0", ServiceName: "test", Environment: "test", AuthTokenSecret: "router-mcp-test-secret-with-enough-entropy",
		MCPStdioServersJSON: `[{"name":"approved-local","command":"/usr/local/bin/approved-mcp","allowed_tools":["safe.search"],"timeout_ms":5000,"enabled":true}]`,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	token := registerSkillUser(t, server, "router-mcp@example.com", "router-mcp")

	routed := performJSON(t, server, http.MethodPost, "/v1/intent/route", token, map[string]any{"text": "生成 8 页 PPT 给管理层", "page": "work"})
	if routed.Code != http.StatusOK || !strings.Contains(routed.Body.String(), `"intent":"office"`) || !strings.Contains(routed.Body.String(), `"suggested_skill":"office.pptx_generate"`) || !strings.Contains(routed.Body.String(), `"risk_level":"none"`) || !strings.Contains(routed.Body.String(), `"slide_count":8`) {
		t.Fatalf("route=%d %s", routed.Code, routed.Body.String())
	}
	invalid := performJSON(t, server, http.MethodPost, "/v1/intent/route", token, map[string]any{"text": ""})
	if invalid.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid route=%d %s", invalid.Code, invalid.Body.String())
	}
	catalog := performJSON(t, server, http.MethodGet, "/v1/mcp/servers", token, nil)
	if catalog.Code != http.StatusOK || !strings.Contains(catalog.Body.String(), `"name":"approved-local"`) || !strings.Contains(catalog.Body.String(), `"safe.search"`) || strings.Contains(catalog.Body.String(), "command") || strings.Contains(catalog.Body.String(), "environment") {
		t.Fatalf("catalog=%d %s", catalog.Code, catalog.Body.String())
	}
}
