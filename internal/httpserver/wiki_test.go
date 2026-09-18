package httpserver

import (
	"context"
	"encoding/json"
	"github.com/windcry1/ai-companion/internal/character"
	"github.com/windcry1/ai-companion/internal/conversation"
	"github.com/windcry1/ai-companion/internal/document"
	"github.com/windcry1/ai-companion/internal/identity"
	"github.com/windcry1/ai-companion/internal/memory"
	"github.com/windcry1/ai-companion/internal/platform/config"
	"github.com/windcry1/ai-companion/internal/realtime"
	"github.com/windcry1/ai-companion/internal/semantic"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

type wikiHTTPParser struct{}

func (wikiHTTPParser) Parse(_ context.Context, _ string, data []byte) (document.ParseResult, error) {
	return document.ParseResult{ParserVersion: "test-v1", Pages: []document.Page{{PageNo: 1, Text: string(data)}}, Chunks: []document.Chunk{{Ordinal: 0, PageStart: 1, PageEnd: 1, SectionPath: "项目决策", Content: string(data)}}}, nil
}
func TestWikiHTTPAuthEditSearchExportAndDeletion(t *testing.T) {
	store := document.NewMemoryStore()
	blobs := document.NewMemoryBlobStore()
	server := NewWithDocumentDependencies(config.Config{Environment: "test", AuthTokenSecret: "context-test-only-secret", Knowledge: semantic.Config{WikiEnabled: true}}, slog.New(slog.NewTextHandler(io.Discard, nil)), identity.NewMemoryStore(), character.NewMemoryStore(), conversation.NewMemoryStore(), memory.NewMemoryStore(), store, blobs, conversation.DevelopmentProvider{}, realtime.NewMemoryGateway())
	register := func(email string) string {
		r := performJSON(t, server, http.MethodPost, "/v1/auth/register", "", map[string]any{"email": email, "password": "correct-horse-battery", "display_name": "Wiki测试", "timezone": "Asia/Shanghai", "device": map[string]any{"device_key": email, "name": "test", "platform": "web"}})
		if r.Code != 201 {
			t.Fatalf("register %d %s", r.Code, r.Body.String())
		}
		var result struct {
			Token string `json:"access_token"`
		}
		_ = json.Unmarshal(r.Body.Bytes(), &result)
		return result.Token
	}
	token := register("wiki-test@example.com")
	other := register("wiki-other@example.com")
	upload := performUpload(t, server, token, "计划.txt", []byte("预算：100元\n必须保留来源"))
	var uploaded struct {
		Document document.Document `json:"document"`
	}
	if err := json.Unmarshal(upload.Body.Bytes(), &uploaded); err != nil || uploaded.Document.ID == "" {
		t.Fatal(upload.Body.String())
	}
	ctx := context.Background()
	ingestor := document.NewIngestor(store, blobs, wikiHTTPParser{}, document.NoopVectorIndex{}, "test")
	if _, err := ingestor.RunJob(ctx, uploaded.Document.JobID); err != nil {
		t.Fatal(err)
	}
	if _, err := server.documents.CompileNextWiki(ctx); err != nil {
		t.Fatal(err)
	}
	listing := performJSON(t, server, "GET", "/v1/wiki/pages", token, nil)
	if listing.Code != 200 {
		t.Fatal(listing.Body.String())
	}
	var listed struct {
		Items []document.WikiPage `json:"items"`
	}
	_ = json.Unmarshal(listing.Body.Bytes(), &listed)
	if len(listed.Items) != 2 || listed.Items[0].Body != "" {
		t.Fatal("bad list projection")
	}
	p := listed.Items[0]
	if r := performJSON(t, server, "GET", "/v1/wiki/pages/"+p.ID, other, nil); r.Code != 404 {
		t.Fatal("cross-user page access")
	}
	if r := performJSON(t, server, "GET", "/v1/workspaces/unowned/wiki/pages", other, nil); r.Code == 200 {
		t.Fatal("workspace membership bypass")
	}
	edited := performJSON(t, server, "PATCH", "/v1/wiki/pages/"+p.ID, token, map[string]any{"version": p.Version, "title": "已核对", "body": "预算：100元"})
	if edited.Code != 200 {
		t.Fatal(edited.Body.String())
	}
	conflict := performJSON(t, server, "PATCH", "/v1/wiki/pages/"+p.ID, token, map[string]any{"version": p.Version, "title": "覆盖", "body": "bad"})
	if conflict.Code != 409 {
		t.Fatal("missing version conflict")
	}
	search := performJSON(t, server, "POST", "/v1/wiki/search", token, map[string]any{"query": "预算", "limit": 5})
	if search.Code != 200 || !strings.Contains(search.Body.String(), "100元") {
		t.Fatal(search.Body.String())
	}
	export := performJSON(t, server, "GET", "/v1/wiki/pages/"+p.ID+"/export", token, nil)
	if export.Code != 200 || !strings.Contains(export.Body.String(), "[chunk:") {
		t.Fatal("export missing citations")
	}
	feedback := performJSON(t, server, "POST", "/v1/wiki/pages/"+p.ID+"/feedback", token, map[string]any{"version": 2, "rating": "helpful"})
	if feedback.Code != 204 {
		t.Fatal(feedback.Body.String())
	}
	if r := performJSON(t, server, "DELETE", "/v1/documents/"+uploaded.Document.ID, token, nil); r.Code != 204 {
		t.Fatal(r.Body.String())
	}
	if r := performJSON(t, server, "GET", "/v1/wiki/pages/"+p.ID, token, nil); r.Code != 404 {
		t.Fatal("deleted reference readable")
	}
}
