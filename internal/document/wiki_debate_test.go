package document

import (
	"context"
	"encoding/json"
	"github.com/windcry1/ai-companion/internal/arbitration"
	"github.com/windcry1/ai-companion/internal/semantic"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestWikiDebateReadsOnlyFrozenAttachedSourcesAndReusesCheckpoint(t *testing.T) {
	s, store, ingestor := wikiFixture(t)
	a := ingestWiki(t, s, ingestor, "alice", "a.txt", "预算：100元")
	b := ingestWiki(t, s, ingestor, "alice", "b.txt", "预算：200元")
	_ = ingestWiki(t, s, ingestor, "alice", "unrelated.txt", "预算：PRIVATE 未被授权加入当前页面的来源")
	store.mu.Lock()
	store.chunks[a.ID] = append(store.chunks[a.ID], Chunk{ID: "footnote", Ordinal: 2, Content: "预算最终批复为100元，200元为草案"})
	a.ChunkCount++
	store.items[a.ID] = a
	store.mu.Unlock()
	ca, _ := store.ListDocumentChunks(context.Background(), "alice", a.ID, 10)
	cb, _ := store.ListDocumentChunks(context.Background(), "alice", b.ID, 10)
	input := arbitration.Input{Kind: "wiki", Issues: []arbitration.Issue{{ID: "budget", Label: "预算", Candidates: []arbitration.Candidate{{ID: "a", Text: "预算100元", EvidenceIDs: []string{ca[0].ID}}, {ID: "b", Text: "预算200元", EvidenceIDs: []string{cb[0].ID}}}}}, Evidence: []arbitration.Evidence{{ID: ca[0].ID, Text: "预算：100元", Version: SourceVersion(a)}, {ID: cb[0].ID, Text: "预算：200元", Version: SourceVersion(b)}}}
	pages := []WikiPage{{OwnerID: "alice", Sources: []WikiSource{{DocumentID: a.ID, Version: SourceVersion(a)}, {DocumentID: b.ID, Version: SourceVersion(b)}}}}
	var calls atomic.Int32
	var sawSupplement atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		data := request.Messages[len(request.Messages)-1].Content
		if strings.Contains(data, "PRIVATE") {
			t.Error("read outside frozen source intersection")
		}
		var packet struct {
			Focus  string `json:"focus"`
			Packet struct {
				Input arbitration.Input   `json:"input"`
				New   map[string][]string `json:"new_evidence_issues"`
			} `json:"packet"`
		}
		_ = json.Unmarshal([]byte(data), &packet)
		var output any
		if packet.Focus != "" {
			p := arbitration.Position{Assessments: []arbitration.Assessment{}, Novelty: []arbitration.Novelty{}}
			for _, i := range packet.Packet.Input.Issues {
				for _, c := range i.Candidates {
					p.Assessments = append(p.Assessments, arbitration.Assessment{IssueID: i.ID, CandidateID: c.ID, Stance: "uncertain", Reason: "仍缺适用范围", Evidence: []arbitration.Citation{}})
				}
			}
			if len(packet.Packet.New) > 0 {
				sawSupplement.Store(true)
			}
			output = p
		} else {
			output = map[string]any{"decisions": []arbitration.Decision{{IssueID: "budget", Action: "need_evidence", Classification: "unresolved", SelectedIDs: []string{}, Reason: "核对预算批复", Evidence: []arbitration.Citation{}, EvidenceRequests: []arbitration.EvidenceRequest{{Question: "预算", Discriminates: "批复或草案", ToolName: "read_original", Arguments: map[string]any{"query": "预算"}}}}}}
		}
		encoded, _ := json.Marshal(output)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(encoded)}}}})
	}))
	defer server.Close()
	client := semantic.New(semantic.Config{BaseURL: server.URL, SummaryModel: "mock"})
	result := s.debateWiki(context.Background(), pages, input, client)
	if result.Calls != 5 || !sawSupplement.Load() || result.TerminationReason != "no_new_evidence" {
		t.Fatalf("%+v", result)
	}
	again := s.debateWiki(context.Background(), pages, input, client)
	if calls.Load() != 5 || again.Calls != 5 {
		t.Fatal("completed checkpoint replayed model calls")
	}
	pages[0].Sources[0].Version = "outdated"
	invalid := s.debateWiki(context.Background(), pages, input, client)
	if calls.Load() != 5 || invalid.TerminationReason != "evidence_access_changed" {
		t.Fatal("cached results bypassed version verification")
	}
}
