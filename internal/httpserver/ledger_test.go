package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/windcry1/ai-companion/internal/ledger"
	"github.com/windcry1/ai-companion/internal/platform/config"
)

type httpLedgerExporter struct{}

func (httpLedgerExporter) Export(context.Context, ledger.ExportPayload) ([]byte, error) {
	return []byte("PK-http-workbook"), nil
}

func TestLedgerCandidateConfirmationAndSummaryAPI(t *testing.T) {
	server := New(config.Config{HTTPAddr: ":0", ServiceName: "test", Environment: "test", AuthTokenSecret: "ledger-test-secret-with-enough-entropy"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	register := performJSON(t, server, http.MethodPost, "/v1/auth/register", "", map[string]any{
		"email": "ledger@example.com", "password": "correct-horse-battery", "display_name": "账本用户", "timezone": "Asia/Shanghai",
		"device": map[string]any{"device_key": "ledger-browser", "name": "Ledger Browser", "platform": "web"},
	})
	var tokens struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(register.Body.Bytes(), &tokens); err != nil {
		t.Fatal(err)
	}
	character := createCharacter(t, server, tokens.AccessToken, map[string]any{"name": "小棉", "personality": "温柔", "speech_style": "短句"})
	conversationResponse := performJSON(t, server, http.MethodPost, "/v1/conversations", tokens.AccessToken, map[string]string{"character_id": character.Character.ID})
	var conversation struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(conversationResponse.Body.Bytes(), &conversation); err != nil {
		t.Fatal(err)
	}
	chat := performJSON(t, server, http.MethodPost, "/v1/conversations/"+conversation.ID+"/messages", tokens.AccessToken, map[string]string{"content": "昨晚打车 36 元"})
	if chat.Code != http.StatusAccepted || !strings.Contains(chat.Body.String(), `"ledger_candidate"`) || strings.Contains(chat.Body.String(), `"ledger_entry"`) {
		t.Fatalf("chat candidate = %d %s", chat.Code, chat.Body.String())
	}
	candidateResponse := performJSON(t, server, http.MethodPost, "/v1/ledger/candidates", tokens.AccessToken, map[string]string{"text": "昨晚打车 36 元"})
	if candidateResponse.Code != http.StatusCreated || !strings.Contains(candidateResponse.Body.String(), `"amount_minor":3600`) || !strings.Contains(candidateResponse.Body.String(), `"category":"transport"`) {
		t.Fatalf("candidate = %d %s", candidateResponse.Code, candidateResponse.Body.String())
	}
	var candidate struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(candidateResponse.Body.Bytes(), &candidate); err != nil {
		t.Fatal(err)
	}
	missingKey := performJSON(t, server, http.MethodPost, "/v1/ledger/candidates/"+candidate.ID+"/confirm", tokens.AccessToken, map[string]string{"note": "客户拜访"})
	if missingKey.Code != http.StatusBadRequest {
		t.Fatalf("missing idempotency key = %d %s", missingKey.Code, missingKey.Body.String())
	}
	confirmed := performLedgerConfirm(t, server, tokens.AccessToken, candidate.ID, "confirm-taxi")
	if confirmed.Code != http.StatusCreated {
		t.Fatalf("confirm = %d %s", confirmed.Code, confirmed.Body.String())
	}
	duplicate := performLedgerConfirm(t, server, tokens.AccessToken, candidate.ID, "confirm-taxi")
	if duplicate.Code != http.StatusOK || !strings.Contains(duplicate.Body.String(), `"deduplicated":true`) {
		t.Fatalf("duplicate = %d %s", duplicate.Code, duplicate.Body.String())
	}
	items := performJSON(t, server, http.MethodGet, "/v1/ledger/entries", tokens.AccessToken, nil)
	if items.Code != http.StatusOK || strings.Count(items.Body.String(), `"amount_minor":3600`) != 1 {
		t.Fatalf("entries = %d %s", items.Code, items.Body.String())
	}
	summary := performJSON(t, server, http.MethodGet, "/v1/ledger/summary?month=2026-07&timezone=Asia%2FShanghai&currency=CNY", tokens.AccessToken, nil)
	if summary.Code != http.StatusOK || !strings.Contains(summary.Body.String(), `"expense_minor":3600`) || !strings.Contains(summary.Body.String(), `"entry_count":1`) {
		t.Fatalf("summary = %d %s", summary.Code, summary.Body.String())
	}
	server.SetLedgerExporter(httpLedgerExporter{})
	export := performLedgerExport(t, server, tokens.AccessToken, "export-2026-07")
	if export.Code != http.StatusAccepted {
		t.Fatalf("export = %d %s", export.Code, export.Body.String())
	}
	var exportEnvelope struct {
		Export ledger.ExportJob `json:"export"`
	}
	_ = json.Unmarshal(export.Body.Bytes(), &exportEnvelope)
	if processed, err := server.ledger.RunExport(context.Background(), exportEnvelope.Export.ID, "test-worker", time.Minute); err != nil || !processed {
		t.Fatalf("run export processed=%v err=%v", processed, err)
	}
	download := performJSON(t, server, http.MethodGet, "/v1/ledger/exports/"+exportEnvelope.Export.ID+"/file", tokens.AccessToken, nil)
	if download.Code != http.StatusOK || download.Header().Get("Content-Type") != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" || download.Body.String() != "PK-http-workbook" {
		t.Fatalf("download = %d %s %q", download.Code, download.Header().Get("Content-Type"), download.Body.String())
	}
}

func performLedgerExport(t *testing.T, server *Server, token, key string) *httptest.ResponseRecorder {
	t.Helper()
	payload, _ := json.Marshal(map[string]string{"month": "2026-07", "timezone": "Asia/Shanghai", "currency": "CNY"})
	request := httptest.NewRequest(http.MethodPost, "/v1/ledger/exports", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	return response
}

func performLedgerConfirm(t *testing.T, server *Server, token, candidateID, key string) *httptest.ResponseRecorder {
	t.Helper()
	payload, _ := json.Marshal(map[string]string{"note": "客户拜访"})
	request := httptest.NewRequest(http.MethodPost, "/v1/ledger/candidates/"+candidateID+"/confirm", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	return response
}
