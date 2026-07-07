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

	"github.com/windcry1/ai-companion/internal/platform/config"
)

func TestSkillRuntimeAPIConfirmationIdempotencyAndDownload(t *testing.T) {
	server := New(config.Config{HTTPAddr: ":0", ServiceName: "test", Environment: "test", AuthTokenSecret: "skill-api-test-secret-with-enough-entropy"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	register := performJSON(t, server, http.MethodPost, "/v1/auth/register", "", map[string]any{
		"email": "skill@example.com", "password": "correct-horse-battery", "display_name": "Skill 用户", "timezone": "Asia/Shanghai",
		"device": map[string]any{"device_key": "skill-web", "name": "Skill Web", "platform": "web"},
	})
	var tokens struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.Unmarshal(register.Body.Bytes(), &tokens)

	manifests := performJSON(t, server, http.MethodGet, "/v1/skills", tokens.AccessToken, nil)
	if manifests.Code != http.StatusOK || !strings.Contains(manifests.Body.String(), `"office.translate"`) || !strings.Contains(manifests.Body.String(), `"office.markdown_document"`) || !strings.Contains(manifests.Body.String(), `"office.docx_edit"`) || !strings.Contains(manifests.Body.String(), `"office.pptx_outline"`) || !strings.Contains(manifests.Body.String(), `"office.pptx_generate"`) || !strings.Contains(manifests.Body.String(), `"office.tabular_profile"`) {
		t.Fatalf("skills=%d %s", manifests.Code, manifests.Body.String())
	}
	disabled := performJSON(t, server, http.MethodPut, "/v1/skills/office.translate/settings", tokens.AccessToken, map[string]any{"enabled": false})
	if disabled.Code != http.StatusOK || !strings.Contains(disabled.Body.String(), `"enabled":false`) {
		t.Fatalf("disable=%d %s", disabled.Code, disabled.Body.String())
	}
	blocked := performSkillRequest(t, server, http.MethodPost, "/v1/skills/office.translate/runs", tokens.AccessToken, "translate-disabled", map[string]any{"input": map[string]any{"text": "你好", "target_language": "English"}})
	if blocked.Code != http.StatusForbidden || !strings.Contains(blocked.Body.String(), `"skill_disabled"`) {
		t.Fatalf("disabled start=%d %s", blocked.Code, blocked.Body.String())
	}
	enabled := performJSON(t, server, http.MethodPut, "/v1/skills/office.translate/settings", tokens.AccessToken, map[string]any{"enabled": true})
	if enabled.Code != http.StatusOK || !strings.Contains(enabled.Body.String(), `"enabled":true`) {
		t.Fatalf("enable=%d %s", enabled.Code, enabled.Body.String())
	}
	invalidSetting := performJSON(t, server, http.MethodPut, "/v1/skills/office.translate/settings", tokens.AccessToken, map[string]any{})
	if invalidSetting.Code != http.StatusUnprocessableEntity || !strings.Contains(invalidSetting.Body.String(), `"validation_error"`) {
		t.Fatalf("invalid setting=%d %s", invalidSetting.Code, invalidSetting.Body.String())
	}
	presentationCandidate := performSkillRequest(t, server, http.MethodPost, "/v1/skills/office.pptx_generate/runs", tokens.AccessToken, "pptx-http-1", map[string]any{"input": map[string]any{
		"title": "季度复盘", "audience": "管理层", "style": "简洁专业", "brief": "业绩亮点\n风险与机会", "slide_count": 6,
	}})
	if presentationCandidate.Code != http.StatusAccepted || !strings.Contains(presentationCandidate.Body.String(), `"status":"waiting_confirmation"`) || !strings.Contains(presentationCandidate.Body.String(), `"audience":"管理层"`) || !strings.Contains(presentationCandidate.Body.String(), `"files":[]`) {
		t.Fatalf("pptx candidate=%d %s", presentationCandidate.Code, presentationCandidate.Body.String())
	}

	translated := performSkillRequest(t, server, http.MethodPost, "/v1/skills/office.translate/runs", tokens.AccessToken, "translate-http-1", map[string]any{"input": map[string]any{"text": "项目已经完成", "target_language": "English"}})
	if translated.Code != http.StatusCreated || !strings.Contains(translated.Body.String(), `"status":"succeeded"`) || !strings.Contains(translated.Body.String(), `"The project has been completed."`) {
		t.Fatalf("translate=%d %s", translated.Code, translated.Body.String())
	}
	replayedTranslation := performSkillRequest(t, server, http.MethodPost, "/v1/skills/office.translate/runs", tokens.AccessToken, "translate-http-1", map[string]any{"input": map[string]any{"text": "ignored", "target_language": "English"}})
	if replayedTranslation.Code != http.StatusOK || !strings.Contains(replayedTranslation.Body.String(), `"deduplicated":true`) {
		t.Fatalf("translate replay=%d %s", replayedTranslation.Code, replayedTranslation.Body.String())
	}

	candidate := performSkillRequest(t, server, http.MethodPost, "/v1/skills/office.markdown_document/runs", tokens.AccessToken, "doc-http-1", map[string]any{"input": map[string]any{"title": "验收文档", "content": "确认后才能创建。"}})
	if candidate.Code != http.StatusAccepted || !strings.Contains(candidate.Body.String(), `"status":"waiting_confirmation"`) || !strings.Contains(candidate.Body.String(), `"files":[]`) {
		t.Fatalf("candidate=%d %s", candidate.Code, candidate.Body.String())
	}
	var candidatePayload struct {
		Run struct {
			ID string `json:"id"`
		} `json:"run"`
	}
	_ = json.Unmarshal(candidate.Body.Bytes(), &candidatePayload)
	confirmed := performSkillRequest(t, server, http.MethodPost, "/v1/skill-runs/"+candidatePayload.Run.ID+"/confirm", tokens.AccessToken, "confirm-http-1", map[string]any{})
	if confirmed.Code != http.StatusOK || !strings.Contains(confirmed.Body.String(), `"source_overwritten":false`) || !strings.Contains(confirmed.Body.String(), `"name":"验收文档.md"`) {
		t.Fatalf("confirm=%d %s", confirmed.Code, confirmed.Body.String())
	}
	var confirmedRun struct {
		Files []struct {
			ID string `json:"id"`
		} `json:"files"`
	}
	_ = json.Unmarshal(confirmed.Body.Bytes(), &confirmedRun)
	duplicate := performSkillRequest(t, server, http.MethodPost, "/v1/skill-runs/"+candidatePayload.Run.ID+"/confirm", tokens.AccessToken, "confirm-http-1", map[string]any{})
	if duplicate.Code != http.StatusOK || len(confirmedRun.Files) != 1 {
		t.Fatalf("duplicate=%d %s", duplicate.Code, duplicate.Body.String())
	}
	download := performJSON(t, server, http.MethodGet, "/v1/skill-runs/"+candidatePayload.Run.ID+"/files/"+confirmedRun.Files[0].ID, tokens.AccessToken, nil)
	if download.Code != http.StatusOK || download.Body.String() != "# 验收文档\n\n确认后才能创建。\n" {
		t.Fatalf("download=%d %q", download.Code, download.Body.String())
	}
}

func TestSkillRunCanCancelRetryAndEnforcesOwnership(t *testing.T) {
	server := New(config.Config{HTTPAddr: ":0", ServiceName: "test", Environment: "test", AuthTokenSecret: "skill-owner-test-secret-with-enough-entropy"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	owner := registerSkillUser(t, server, "skill-owner@example.com", "owner")
	other := registerSkillUser(t, server, "skill-other@example.com", "other")
	candidate := performSkillRequest(t, server, http.MethodPost, "/v1/skills/office.markdown_document/runs", owner, "cancel-doc", map[string]any{"input": map[string]any{"title": "草稿", "content": "内容"}})
	var payload struct {
		Run struct {
			ID string `json:"id"`
		} `json:"run"`
	}
	_ = json.Unmarshal(candidate.Body.Bytes(), &payload)
	cancelled := performSkillRequest(t, server, http.MethodPost, "/v1/skill-runs/"+payload.Run.ID+"/cancel", owner, "cancel-key", map[string]any{})
	if cancelled.Code != http.StatusOK || !strings.Contains(cancelled.Body.String(), `"status":"cancelled"`) {
		t.Fatalf("cancel=%d %s", cancelled.Code, cancelled.Body.String())
	}
	retried := performSkillRequest(t, server, http.MethodPost, "/v1/skill-runs/"+payload.Run.ID+"/retry", owner, "retry-key", map[string]any{})
	if retried.Code != http.StatusOK || !strings.Contains(retried.Body.String(), `"status":"waiting_confirmation"`) || !strings.Contains(retried.Body.String(), `"attempt":2`) {
		t.Fatalf("retry=%d %s", retried.Code, retried.Body.String())
	}
	forbidden := performJSON(t, server, http.MethodGet, "/v1/skill-runs/"+payload.Run.ID, other, nil)
	if forbidden.Code != http.StatusNotFound {
		t.Fatalf("cross-user get=%d %s", forbidden.Code, forbidden.Body.String())
	}
}

func TestLongRunningSkillIsAcceptedIntoWorkerQueue(t *testing.T) {
	server := New(config.Config{HTTPAddr: ":0", ServiceName: "test", Environment: "test", AuthTokenSecret: "skill-queue-test-secret-with-enough-entropy", SkillWorkerEnabled: true}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	token := registerSkillUser(t, server, "skill-queue@example.com", "queue")
	response := performSkillRequest(t, server, http.MethodPost, "/v1/skills/office.pptx_outline/runs", token, "queued-outline", map[string]any{"input": map[string]any{
		"title": "季度复盘", "audience": "管理层", "style": "简洁", "brief": "亮点与风险", "slide_count": 5,
	}})
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"status":"queued"`) || !strings.Contains(response.Body.String(), `"execution_mode":"worker"`) {
		t.Fatalf("queued response=%d %s", response.Code, response.Body.String())
	}
}

func registerSkillUser(t *testing.T, server *Server, email, key string) string {
	t.Helper()
	response := performJSON(t, server, http.MethodPost, "/v1/auth/register", "", map[string]any{
		"email": email, "password": "correct-horse-battery", "display_name": key, "timezone": "Asia/Shanghai",
		"device": map[string]any{"device_key": key, "name": key, "platform": "web"},
	})
	var tokens struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &tokens)
	return tokens.AccessToken
}

func performSkillRequest(t *testing.T, server *Server, method, path, token, key string, body any) *httptest.ResponseRecorder {
	t.Helper()
	payload, _ := json.Marshal(body)
	request := httptest.NewRequest(method, path, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	return response
}
