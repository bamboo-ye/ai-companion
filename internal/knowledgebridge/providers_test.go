package knowledgebridge

import (
	"context"
	"errors"
	"testing"

	"github.com/windcry1/ai-companion/internal/document"
	"github.com/windcry1/ai-companion/internal/knowledge"
	"github.com/windcry1/ai-companion/internal/memory"
	"github.com/windcry1/ai-companion/internal/semantic"
)

func TestMemoryAdapterPendingScopeAndProjectionInvalidation(t *testing.T) {
	ctx := context.Background()
	m := memory.NewService(memory.NewMemoryStore())
	d := document.NewService(document.NewMemoryStore(), document.NewMemoryBlobStore(), 0)
	c := semantic.New(semantic.Config{EmbeddingModel: "fixture", Dimensions: 2})
	m.SetSemanticClient(c)
	item, err := m.Create(ctx, "alice", "喜欢冰岛极光摄影")
	if err != nil {
		t.Fatal(err)
	}
	s := New(d, m, c, nil)
	scope := knowledge.Scope{UserID: "alice", Module: "work"}
	out, err := s.Search(ctx, scope, knowledge.Query{Query: "冰岛极光", Kinds: []string{"memory"}})
	if err != nil || len(out.Hits) != 1 || len(out.Manifest.Degraded) != 1 {
		t.Fatal(out, err)
	}
	parts := c.SourceProjections("alice", "memory", item.ID, memory.KnowledgeRevision(item), item.Content)
	valid := ValidateProjection(d, m, c)
	if ok, err := valid(ctx, parts[0]); err != nil || !ok {
		t.Fatal(ok, err)
	}
	forged := parts[0]
	forged.Owner = "bob"
	if ok, err := valid(ctx, forged); err != nil || ok {
		t.Fatal("owner spoof", ok, err)
	}
	forged = parts[0]
	forged.Text = "插入伪造事实"
	if ok, err := valid(ctx, forged); err != nil || ok {
		t.Fatal("source tamper", ok, err)
	}
	if _, err = m.Correct(ctx, "alice", item.ID, "不再喜欢冰岛极光摄影"); err != nil {
		t.Fatal(err)
	}
	if ok, err := valid(ctx, parts[0]); err != nil || ok {
		t.Fatal("obsolete worker accepted", ok, err)
	}
	if _, err = s.Read(ctx, scope, knowledge.ReadInput{Ref: out.Hits[0].Ref}); !errors.Is(err, knowledge.ErrNotFound) {
		t.Fatal("superseded reference accepted", err)
	}
}
