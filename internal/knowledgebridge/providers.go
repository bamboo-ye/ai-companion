// Package knowledgebridge composes domain providers without dependency cycles.
package knowledgebridge

import (
	"context"
	"errors"
	"github.com/windcry1/ai-companion/internal/document"
	"github.com/windcry1/ai-companion/internal/knowledge"
	"github.com/windcry1/ai-companion/internal/memory"
	"github.com/windcry1/ai-companion/internal/productknowledge"
	"github.com/windcry1/ai-companion/internal/semantic"
)

type provider struct {
	kind      string
	documents *document.Service
	memories  *memory.Service
	client    *semantic.Client
}

func New(d *document.Service, m *memory.Service, c *semantic.Client, authorize func(context.Context, knowledge.Scope) error) *knowledge.Service {
	s := knowledge.New(authorize)
	for _, kind := range []string{"document", "wiki", "memory", "builtin"} {
		s.Register(kind, &provider{kind, d, m, c})
	}
	return s
}
func (p *provider) Search(ctx context.Context, s knowledge.Scope, q knowledge.Query) (knowledge.Batch, error) {
	batch := knowledge.Batch{Items: []knowledge.Item{}, Degraded: []string{}}
	if p.client != nil && p.client.EmbeddingsEnabled() {
		batch.IndexSpec = p.client.ProjectionSpec()
	} else {
		batch.IndexSpec = "lexical-v1"
	}
	switch p.kind {
	case "builtin":
		hits, err := productknowledge.Search(q.Query, 12)
		if err != nil {
			return batch, err
		}
		batch.IndexSpec = productknowledge.Revision()
		for _, hit := range hits {
			batch.Items = append(batch.Items, builtinItem(hit.Page))
		}
	case "memory":
		if p.memories == nil {
			return batch, nil
		}
		hits, degraded, err := p.memories.RecallKnowledge(ctx, s.UserID, q.Query, 20)
		if err != nil {
			return batch, err
		}
		for _, hit := range hits {
			item, err := p.Read(ctx, s, knowledge.Ref{Kind: "memory", SourceID: hit.Source.ID})
			if err == nil {
				batch.Items = append(batch.Items, item)
			}
		}
		if degraded {
			batch.Degraded = append(batch.Degraded, "memory_index_or_model_unavailable")
		}

	case "document":
		if p.documents == nil {
			return batch, nil
		}
		var result document.QueryResult
		var err error
		batch.IndexSpec = p.documents.DocumentIndexSpec()
		pending, pendingErr := p.documents.DocumentIndexPending(ctx, s.UserID, s.WorkspaceID, q.DocumentIDs)
		if pending || pendingErr != nil {
			batch.Degraded = append(batch.Degraded, "document_index_pending")
		}
		input := document.QueryInput{Query: q.Query, DocumentIDs: q.DocumentIDs, Limit: 8}
		if s.WorkspaceID == "" {
			result, err = p.documents.Query(ctx, s.UserID, input)
		} else {
			result, err = p.documents.QueryWorkspace(ctx, s.WorkspaceID, input)
		}
		if err != nil {
			return batch, err
		}
		if result.Degraded {
			batch.Degraded = append(batch.Degraded, result.DegradationReason)
		}
		for _, hit := range result.Citations {
			item, err := p.Read(ctx, s, knowledge.Ref{Kind: "document", SourceID: hit.DocumentID, Revision: hit.SourceVersion, FragmentID: hit.ChunkID})
			if err == nil {
				batch.Items = append(batch.Items, item)
			}
		}
	case "wiki":
		if p.documents == nil {
			return batch, nil
		}
		result, err := p.documents.SearchWiki(ctx, s.UserID, s.WorkspaceID, q.Query, 8, 6000)
		if err != nil {
			return batch, err
		}
		if result.Degraded {
			batch.Degraded = append(batch.Degraded, "wiki_index_or_reranker_unavailable")
		}
		wanted := map[string]bool{}
		for _, id := range q.DocumentIDs {
			wanted[id] = true
		}
		for _, hit := range result.Hits {
			if len(wanted) > 0 {
				allowed := true
				for _, source := range hit.Page.Sources {
					if !wanted[source.DocumentID] {
						allowed = false
					}
				}
				if !allowed {
					continue
				}
			}
			batch.Items = append(batch.Items, knowledge.Item{Ref: knowledge.Ref{Kind: "wiki", SourceID: hit.Page.ID, Revision: hit.Revision}})
		}
	}
	return batch, nil
}
func builtinItem(page productknowledge.Page) knowledge.Item {
	return knowledge.Item{Ref: knowledge.Ref{Kind: "builtin", SourceID: page.ID, Revision: page.Version, ContentHash: knowledge.Hash(page.Body), Locator: knowledge.Locator{Section: page.Section}}, Title: page.Title, Content: page.Body, CitationURL: page.CitationURL, EvidenceState: "unassessed"}
}
func (p *provider) Read(ctx context.Context, s knowledge.Scope, ref knowledge.Ref) (knowledge.Item, error) {
	switch p.kind {
	case "builtin":
		page, err := productknowledge.Read(ref.SourceID)
		if err != nil {
			return knowledge.Item{}, knowledge.ErrNotFound
		}
		return builtinItem(page), nil
	case "memory":
		if p.memories == nil {
			return knowledge.Item{}, knowledge.ErrNotFound
		}
		m, err := p.memories.KnowledgeMemory(ctx, s.UserID, ref.SourceID)
		if err != nil {
			return knowledge.Item{}, translate(err)
		}
		state := "unassessed"
		if m.Arbitration != nil && len(m.Arbitration.Clarifications) > 0 {
			state = "disputed"
		}
		return knowledge.Item{Ref: knowledge.Ref{Kind: "memory", SourceID: m.ID, Revision: memory.KnowledgeRevision(m), ContentHash: knowledge.Hash(m.Content)}, Title: "长期记忆", Content: m.Content, EvidenceState: state, ValidFrom: &m.ValidFrom, ValidTo: m.ValidTo}, nil
	case "wiki":
		if p.documents == nil {
			return knowledge.Item{}, knowledge.ErrNotFound
		}
		page, err := p.documents.ReadWiki(ctx, s.UserID, s.WorkspaceID, ref.SourceID)
		if err != nil {
			return knowledge.Item{}, translate(err)
		}
		item := knowledge.Item{Ref: knowledge.Ref{Kind: "wiki", SourceID: page.ID, Revision: document.WikiRevision(page), ContentHash: knowledge.Hash(page.Body)}, Title: page.Title, Content: page.Body, EvidenceState: "unassessed", Dependencies: []knowledge.Ref{}}
		if len(page.Conflicts) > 0 {
			item.EvidenceState = "disputed"
		}
		versions := map[string]string{}
		for _, source := range page.Sources {
			versions[source.DocumentID] = source.Version
		}
		for _, e := range page.Evidence {
			item.Dependencies = append(item.Dependencies, knowledge.Ref{Kind: "document", SourceID: e.DocumentID, Revision: versions[e.DocumentID], FragmentID: e.ChunkID, Locator: knowledge.Locator{PageStart: e.Page}})
		}
		return item, nil
	case "document":
		if p.documents == nil {
			return knowledge.Item{}, knowledge.ErrNotFound
		}
		d, chunks, err := p.documents.KnowledgeChunks(ctx, s.UserID, s.WorkspaceID, ref.SourceID)
		if err != nil {
			return knowledge.Item{}, translate(err)
		}
		for _, chunk := range chunks {
			if chunk.ID == ref.FragmentID {
				return knowledge.Item{Ref: knowledge.Ref{Kind: "document", SourceID: d.ID, Revision: document.SourceVersion(d), FragmentID: chunk.ID, ContentHash: knowledge.Hash(chunk.Content), Locator: knowledge.Locator{PageStart: chunk.PageStart, PageEnd: chunk.PageEnd, Section: chunk.SectionPath}}, Title: d.Name, Content: chunk.Content, EvidenceState: "unassessed"}, nil
			}
		}
		return knowledge.Item{}, knowledge.ErrNotFound
	}
	return knowledge.Item{}, knowledge.ErrValidation
}
func translate(err error) error {
	if errors.Is(err, document.ErrWikiConflict) {
		return knowledge.ErrConflict
	}
	if errors.Is(err, document.ErrNotFound) || errors.Is(err, memory.ErrNotFound) || errors.Is(err, document.ErrParsedContentUnavailable) {
		return knowledge.ErrNotFound
	}
	return err
}

// ValidateProjection rejects stale fragments before model calls and publication.
func ValidateProjection(d *document.Service, m *memory.Service, c *semantic.Client) func(context.Context, semantic.Projection) (bool, error) {
	return func(ctx context.Context, p semantic.Projection) (bool, error) {
		var revision, text string
		switch p.Kind {
		case "wiki":
			page, err := d.ReadWiki(ctx, p.Owner, "", p.SourceID)
			if errors.Is(err, document.ErrNotFound) || errors.Is(err, document.ErrWikiConflict) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			revision = document.WikiRevision(page)
			text = page.Title + "\n" + page.Body
		case "memory":
			item, err := m.KnowledgeMemory(ctx, p.Owner, p.SourceID)
			if errors.Is(err, memory.ErrNotFound) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			revision = memory.KnowledgeRevision(item)
			text = item.Content
		default:
			return false, nil
		}
		if revision != p.Revision || p.Spec != c.ProjectionSpec() {
			return false, nil
		}
		for _, expected := range c.SourceProjections(p.Owner, p.Kind, p.SourceID, revision, text) {
			if expected.Key == p.Key && expected.Text == p.Text {
				return true, nil
			}
		}
		return false, nil
	}
}

func ErrorCode(err error) string {
	switch {
	case errors.Is(err, knowledge.ErrForbidden):
		return "knowledge_forbidden"
	case errors.Is(err, knowledge.ErrNotFound):
		return "knowledge_not_found"
	case errors.Is(err, knowledge.ErrConflict):
		return "knowledge_revision_conflict"
	case errors.Is(err, knowledge.ErrValidation):
		return "knowledge_validation"
	default:
		return "knowledge_unavailable"
	}
}
