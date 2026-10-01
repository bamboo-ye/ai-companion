package knowledge

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type testProvider struct {
	items       []Item
	unavailable bool
	calls       int
	reads       int
}

func (p *testProvider) Search(context.Context, Scope, Query) (Batch, error) {
	p.calls++
	if p.unavailable {
		return Batch{}, errors.New("offline")
	}
	return Batch{Items: p.items, IndexSpec: "fixture"}, nil
}
func (p *testProvider) Read(_ context.Context, s Scope, ref Ref) (Item, error) {
	p.reads++
	if s.UserID != "alice" {
		return Item{}, ErrNotFound
	}
	for _, item := range p.items {
		if item.Ref.SourceID == ref.SourceID {
			return item, nil
		}
	}
	return Item{}, ErrNotFound
}
func fixture() (*Service, *testProvider, Scope) {
	p := &testProvider{items: []Item{{Ref: Ref{Kind: "document", SourceID: "doc", Revision: "v1", FragmentID: "chunk"}, Title: "原文", Content: strings.Repeat("中文证据。", 700), EvidenceState: "unassessed"}}}
	s := New(nil)
	s.Register("document", p)
	return s, p, Scope{UserID: "alice", Module: "work"}
}
func TestEvidenceReadContinuationValidationAndRevision(t *testing.T) {
	ctx := context.Background()
	s, p, scope := fixture()
	result, err := s.Search(ctx, scope, Query{Query: "证据", Kinds: []string{"document"}})
	if err != nil || len(result.Hits) != 1 || !result.Hits[0].Truncated || result.Manifest.EvidenceState != "unassessed" {
		t.Fatalf("search: %+v %v", result, err)
	}
	ref := result.Hits[0].Ref
	if ref.ContentHash != Hash(p.items[0].Content) {
		t.Fatal("hash must cover full source, not preview")
	}
	first, err := s.Read(ctx, scope, ReadInput{Ref: ref})
	if err != nil || !first.HasMore {
		t.Fatalf("first: %+v %v", first, err)
	}
	second, err := s.Read(ctx, scope, ReadInput{Ref: ref, Offset: first.NextOffset})
	if err != nil || second.HasMore || first.Item.Content+second.Item.Content != p.items[0].Content {
		t.Fatal("continuation loses content", err)
	}
	if err = s.Validate(ctx, scope, []Evidence{{Ref: ref, Quote: "中文证据。"}}); err != nil {
		t.Fatal(err)
	}
	if err = s.Validate(ctx, scope, []Evidence{{Ref: ref, Quote: "来源从未说过这句话"}}); !errors.Is(err, ErrConflict) {
		t.Fatal("forged quote accepted")
	}
	p.items[0].Ref.Revision = "v2"
	if _, err = s.Read(ctx, scope, ReadInput{Ref: ref, Offset: first.NextOffset}); !errors.Is(err, ErrConflict) {
		t.Fatal("mixed source versions")
	}
	p.items = nil
	if err = s.Validate(ctx, scope, []Evidence{{Ref: ref, Quote: "中文"}}); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted source validated")
	}
}
func TestKnowledgeScopesFailBeforeProvider(t *testing.T) {
	ctx := context.Background()
	s, p, scope := fixture()
	for _, q := range []Query{{Query: "测试", Kinds: []string{"document", "document"}}, {Query: "测试", Kinds: []string{"document"}, Mode: "global"}, {Query: "测试", Kinds: []string{"unknown"}}, {Query: "测试", Kinds: []string{"document"}, Budget: 100}, {Query: "测试", Kinds: []string{"document"}, WorkspaceID: "forged"}} {
		if _, err := s.Search(ctx, scope, q); err == nil {
			t.Fatalf("accepted %+v", q)
		}
	}
	for _, sc := range []Scope{{UserID: "alice", Module: "life"}, {UserID: "alice", Module: "work", WorkspaceID: "team"}, {Module: "work"}} {
		if _, err := s.Search(ctx, sc, Query{Query: "测试", Kinds: []string{"document"}, WorkspaceID: sc.WorkspaceID}); err == nil {
			t.Fatal("scope accepted", sc)
		}
	}
	if p.calls != 0 {
		t.Fatal("authorization ran after retrieval")
	}
	if _, err := s.Read(ctx, Scope{UserID: "bob", Module: "work"}, ReadInput{Ref: p.items[0].Ref}); !errors.Is(err, ErrNotFound) {
		t.Fatal("other user read")
	}
}
func TestFeedbackReviewAndDegradation(t *testing.T) {
	ctx := context.Background()
	s, p, scope := fixture()
	p.items[0].Ref.ContentHash = Hash(p.items[0].Content)
	f, err := s.Feedback(ctx, scope, Feedback{Ref: p.items[0].Ref, Rating: "incorrect", Note: "需要核对", Status: "resolved", ReviewedBy: "forged"})
	if err != nil || f.Status != "pending" || f.ReviewedBy != "" || f.UserID != "alice" {
		t.Fatal(f, err)
	}
	list, err := s.PendingFeedback(ctx)
	if err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
	if err = s.ResolveFeedback(ctx, f.ID, "operator", "已核对原文并更正"); err != nil {
		t.Fatal(err)
	}
	if err = s.ResolveFeedback(ctx, f.ID, "operator", "重复审核"); !errors.Is(err, ErrConflict) {
		t.Fatal("review CAS missing")
	}
	list, _ = s.PendingFeedback(ctx)
	if len(list) != 0 {
		t.Fatal("resolved item still pending")
	}
	p.unavailable = true
	out, err := s.Search(ctx, scope, Query{Query: "测试", Kinds: []string{"document"}})
	if err != nil || out.Manifest.EvidenceState != "unknown" || len(out.Manifest.Degraded) != 1 {
		t.Fatal(out, err)
	}
}
