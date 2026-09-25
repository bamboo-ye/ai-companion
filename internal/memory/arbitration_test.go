package memory

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

func TestMemoryArbitrationAnnotatesBothSidesAndNeverReplacesWithoutCorrection(t *testing.T) {
	store := NewMemoryStore()
	s := NewService(store)
	ctx := context.Background()
	a, err := s.Create(ctx, "alice", "我喜欢安静的工作环境")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Create(ctx, "alice", "我不喜欢安静的工作环境")
	if err != nil {
		t.Fatal(err)
	}
	if b.Arbitration == nil || b.Arbitration.Status != "retained" || b.SupersedesID != "" {
		t.Fatalf("missing conflict: %+v", b)
	}
	items, _ := s.List(ctx, "alice")
	if len(items) != 2 {
		t.Fatal("unapproved memory replacement")
	}
	hits, _ := s.RecallContext(ctx, "alice", "工作环境", 8)
	if len(hits) != 2 {
		t.Fatal("missing recalled memories")
	}
	for _, hit := range hits {
		if !strings.Contains(hit.Content, "尚未通过明确纠正") {
			t.Fatal("one side lost its dispute marker")
		}
	}
	if err = s.Delete(ctx, "alice", a.ID); err != nil {
		t.Fatal(err)
	}
	items, _ = s.List(ctx, "alice")
	if len(items) != 1 || items[0].Arbitration != nil {
		t.Fatal("deleted source leaked through arbitration")
	}
	c, err := s.Correct(ctx, "alice", b.ID, "现在更喜欢安静的工作环境")
	if err != nil || c.Arbitration != nil || c.SupersedesID != b.ID {
		t.Fatalf("explicit correction: %+v %v", c, err)
	}
}

func TestMemoryScopeJudgmentUsesOnlyOwnerEvidenceAndPreservesBothMemories(t *testing.T) {
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
		decisions := []arbitration.Decision{}
		for _, issue := range input.Issues {
			d := arbitration.Decision{IssueID: issue.ID, Action: "merge", Classification: "scope_difference", Reason: "工作与运动是不同场景", SelectedIDs: []string{}, Evidence: []arbitration.Citation{}}
			for _, c := range issue.Candidates {
				d.SelectedIDs = append(d.SelectedIDs, c.ID)
				d.Evidence = append(d.Evidence, arbitration.Citation{ID: c.EvidenceIDs[0], Quote: c.Text})
				if strings.Contains(c.Text, "秘密") {
					t.Error("another owner's evidence reached model")
				}
			}
			decisions = append(decisions, d)
		}
		data, _ := json.Marshal(map[string]any{"decisions": decisions})
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(data)}}}})
	}))
	defer server.Close()
	s := NewService(NewMemoryStore())
	ctx := context.Background()
	_, _ = s.Create(ctx, "bob", "我喜欢安静的秘密工作环境")
	s.SetSemanticClient(semantic.New(semantic.Config{BaseURL: server.URL, SummaryModel: "mock"}))
	a, _ := s.Create(ctx, "alice", "工作时我喜欢安静的环境")
	b, err := s.Create(ctx, "alice", "运动时我喜欢热闹的环境")
	if err != nil || calls.Load() != 1 || b.Arbitration == nil || b.Arbitration.Decisions[0].Classification != "scope_difference" {
		t.Fatalf("judgment %+v %v calls=%d", b, err, calls.Load())
	}
	if arbitrationNote(b.Arbitration) != "" {
		t.Fatal("different scopes treated as a conflict")
	}
	_, _ = s.Create(ctx, "alice", b.Content)
	if calls.Load() != 1 {
		t.Fatal("duplicate save called model again")
	}
	changed := "工作时我喜欢音乐的环境"
	_, err = s.Update(ctx, "alice", a.ID, UpdateInput{Content: &changed})
	if err != nil {
		t.Fatal(err)
	}
	items, _ := s.List(ctx, "alice")
	for _, item := range items {
		if item.ID == b.ID && item.Arbitration != nil {
			t.Fatal("stale source version reused")
		}
	}
}
