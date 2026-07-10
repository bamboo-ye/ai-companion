package httpserver

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/windcry1/ai-companion/internal/platform/config"
)

func TestMinorModeBlocksRiskySkillsButAllowsSafeSkills(t *testing.T) {
	server := New(config.Config{HTTPAddr: ":0", ServiceName: "test", Environment: "test", AuthTokenSecret: "safety-policy-secret-with-enough-entropy"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	token := registerSkillUser(t, server, "minor-safety@example.com", "minor-safety")

	defaultPolicy := performJSON(t, server, http.MethodGet, "/v1/safety/me", token, nil)
	if defaultPolicy.Code != http.StatusOK || !strings.Contains(defaultPolicy.Body.String(), `"minor_mode":false`) || !strings.Contains(defaultPolicy.Body.String(), `"risky_skills_allowed":true`) {
		t.Fatalf("default policy=%d %s", defaultPolicy.Code, defaultPolicy.Body.String())
	}
	missingGuardian := performJSON(t, server, http.MethodPatch, "/v1/safety/me", token, map[string]any{"minor_mode": true})
	if missingGuardian.Code != http.StatusUnprocessableEntity || !strings.Contains(missingGuardian.Body.String(), "guardian_email") {
		t.Fatalf("missing guardian=%d %s", missingGuardian.Code, missingGuardian.Body.String())
	}
	updated := performJSON(t, server, http.MethodPatch, "/v1/safety/me", token, map[string]any{"minor_mode": true, "guardian_email": "Guardian@Example.com", "risky_skills_allowed": true})
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"minor_mode":true`) || !strings.Contains(updated.Body.String(), `"guardian_email":"guardian@example.com"`) || !strings.Contains(updated.Body.String(), `"risky_skills_allowed":false`) {
		t.Fatalf("updated policy=%d %s", updated.Code, updated.Body.String())
	}
	blocked := performSkillRequest(t, server, http.MethodPost, "/v1/skills/office.markdown_document/runs", token, "minor-risky-doc", map[string]any{"input": map[string]any{"title": "受限", "content": "minor mode should block this"}})
	if blocked.Code != http.StatusForbidden || !strings.Contains(blocked.Body.String(), `"safety_capability_blocked"`) || !strings.Contains(blocked.Body.String(), `"risk_level":"medium"`) {
		t.Fatalf("blocked=%d %s", blocked.Code, blocked.Body.String())
	}
	allowed := performSkillRequest(t, server, http.MethodPost, "/v1/skills/office.translate/runs", token, "minor-safe-translate", map[string]any{"input": map[string]any{"text": "你好", "target_language": "English"}})
	if allowed.Code != http.StatusCreated || !strings.Contains(allowed.Body.String(), `"status":"succeeded"`) {
		t.Fatalf("allowed=%d %s", allowed.Code, allowed.Body.String())
	}
}

func TestAdultCanDisableRiskySkills(t *testing.T) {
	server := New(config.Config{HTTPAddr: ":0", ServiceName: "test", Environment: "test", AuthTokenSecret: "safety-adult-secret-with-enough-entropy"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	token := registerSkillUser(t, server, "adult-safety@example.com", "adult-safety")

	updated := performJSON(t, server, http.MethodPatch, "/v1/safety/me", token, map[string]any{"risky_skills_allowed": false})
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"minor_mode":false`) || !strings.Contains(updated.Body.String(), `"risky_skills_allowed":false`) {
		t.Fatalf("updated=%d %s", updated.Code, updated.Body.String())
	}
	blocked := performSkillRequest(t, server, http.MethodPost, "/v1/skills/office.pptx_generate/runs", token, "adult-risky-ppt", map[string]any{"input": map[string]any{
		"title": "季度复盘", "audience": "管理层", "style": "简洁专业", "brief": "风险与机会", "slide_count": 6,
	}})
	if blocked.Code != http.StatusForbidden || !strings.Contains(blocked.Body.String(), `"skill_name":"office.pptx_generate"`) {
		t.Fatalf("blocked=%d %s", blocked.Code, blocked.Body.String())
	}
}
