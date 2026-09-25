package document

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/windcry1/ai-companion/internal/arbitration"
	"github.com/windcry1/ai-companion/internal/semantic"
)

func TestWikiArbitrationPreservesSourcesAndScopesEachPageRecord(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		var input arbitration.Input
		_ = json.Unmarshal([]byte(body.Messages[len(body.Messages)-1].Content), &input)
		if len(input.Issues) != 1 || len(input.Evidence) != 2 {
			t.Error("model received evidence from another source ACL group")
		}
		decisions := []arbitration.Decision{}
		for _, issue := range input.Issues {
			decisions = append(decisions, arbitration.Decision{IssueID: issue.ID, Action: "need_evidence", Classification: "unresolved", SelectedIDs: []string{}, Reason: "两份原文均未声明替代关系", Evidence: []arbitration.Citation{}})
		}
		data, _ := json.Marshal(map[string]any{"decisions": decisions})
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(data)}}}})
	}))
	defer server.Close()
	s, _, _ := wikiFixture(t)
	s.SetKnowledge(semantic.New(semantic.Config{BaseURL: server.URL, SummaryModel: "mock"}), true)
	pages := []WikiPage{{ID: "budget", Body: "保留预算原文", Sources: []WikiSource{{DocumentID: "doc-a", Version: "a"}, {DocumentID: "doc-b", Version: "b"}}, Conflicts: []string{"预算冲突"}}, {ID: "staff", Body: "保留人数原文", Sources: []WikiSource{{DocumentID: "doc-c", Version: "c"}, {DocumentID: "doc-d", Version: "d"}}, Conflicts: []string{"人数冲突"}}}
	raw := map[string][]WikiEvidence{"budget": {{DocumentID: "doc-a", ChunkID: "a", Quote: "预算：100元"}, {DocumentID: "doc-b", ChunkID: "b", Quote: "预算：200元"}}, "staff": {{DocumentID: "doc-c", ChunkID: "c", Quote: "人数：10人"}, {DocumentID: "doc-d", ChunkID: "d", Quote: "人数：20人"}}}
	s.arbitrateWiki(context.Background(), pages, raw)
	if calls.Load() != 2 {
		t.Fatal("different source ACL dependencies shared a model context")
	}
	for _, p := range pages {
		if p.Arbitration == nil || len(p.Arbitration.Decisions) != 1 || len(p.Arbitration.Inputs) != 2 || !strings.Contains(p.Body, "保留") {
			t.Fatalf("page=%+v", p)
		}
		for _, ref := range p.Arbitration.Inputs {
			if p.ID == "budget" && (ref.ID == "c" || ref.ID == "d") {
				t.Fatal("unrelated page evidence leaked")
			}
		}
	}
	if len(pages[0].Conflicts) != 1 || !strings.Contains(pages[0].Body, "保留预算原文") {
		t.Fatal("arbitration removed the original dispute")
	}
}
