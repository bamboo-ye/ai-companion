package document

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/windcry1/ai-companion/internal/semantic"
)

func (s *Service) indexProjection(d Document) semantic.Projection {
	p := semantic.Projection{Owner: d.UserID, Kind: "document_index", SourceID: d.ID, Revision: SourceVersion(d), Spec: s.DocumentIndexSpec()}
	p.Key = fmt.Sprintf("%x", sha256.Sum256([]byte(p.Owner+"\x00"+p.SourceID+"\x00"+p.Revision+"\x00"+p.Spec)))
	return p
}
func (s *Service) QueueDocumentIndexes(ctx context.Context, user string) error {
	if s.semantic == nil {
		return nil
	}
	docs, err := s.KnowledgeDocuments(ctx, user, "")
	if err != nil {
		return err
	}
	jobs := []semantic.Projection{}
	for _, d := range docs {
		if d.Status == "ready" {
			jobs = append(jobs, s.indexProjection(d))
		}
	}
	return s.semantic.ProjectionStore().Queue(ctx, jobs, time.Now().UTC())
}
func (s *Service) DocumentIndexPending(ctx context.Context, user, workspace string, ids []string) (bool, error) {
	if s.semantic == nil {
		return false, nil
	}
	docs, err := s.KnowledgeDocuments(ctx, user, workspace)
	if err != nil {
		return false, err
	}
	wanted := map[string]bool{}
	for _, id := range ids {
		wanted[id] = true
	}
	keys := []string{}
	for _, d := range docs {
		if d.Status == "ready" && (len(wanted) == 0 || wanted[d.ID]) {
			keys = append(keys, s.indexProjection(d).Key)
		}
	}
	ready, err := s.semantic.ProjectionStore().ReadyKeys(ctx, keys)
	return len(ready) < len(keys), err
}

func (s *Service) annotateIndexStates(ctx context.Context, items []Document) {
	if s.semantic == nil {
		return
	}
	keys := []string{}
	for _, d := range items {
		if d.Status == "ready" {
			keys = append(keys, s.indexProjection(d).Key)
		}
	}
	states, err := s.semantic.ProjectionStore().States(ctx, keys)
	for i := range items {
		if items[i].Status != "ready" {
			continue
		}
		if err != nil {
			items[i].IndexState = "unavailable"
			continue
		}
		switch states[s.indexProjection(items[i]).Key] {
		case "ready":
			items[i].IndexState = "ready"
		case "failed":
			items[i].IndexState = "failed"
		default:
			items[i].IndexState = "pending"
		}
	}
}

// ProcessDocumentIndex publishes a rebuildable projection only after the raw
// source transaction has committed. Long documents renew their claim lease.
func (s *Service) ProcessDocumentIndex(parent context.Context) (bool, error) {
	if s.semantic == nil {
		return false, nil
	}
	store := s.semantic.ProjectionStore()
	p, found, err := store.Claim(parent, s.DocumentIndexSpec(), time.Now().UTC())
	if err != nil || !found {
		return found, err
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	defer func() { cancel(); <-done }()
	go func() {
		defer close(done)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if store.Renew(ctx, p, time.Now().UTC()) != nil {
					cancel()
					return
				}
			}
		}
	}()
	finish := func(code string, cause error) (bool, error) {
		return true, errors.Join(cause, store.Finish(ctx, p, nil, code, time.Now().UTC()))
	}
	d, chunks, err := s.KnowledgeChunks(ctx, p.Owner, "", p.SourceID)
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrParsedContentUnavailable) {
		return finish("obsolete", nil)
	}
	if err != nil {
		return finish("source_unavailable", err)
	}
	if SourceVersion(d) != p.Revision {
		return finish("obsolete", nil)
	}
	if err = publishDocumentIndex(ctx, s.index, d, chunks); err != nil {
		return finish("vector_index_unavailable", err)
	}
	current, err := s.Get(ctx, p.Owner, p.SourceID)
	if errors.Is(err, ErrNotFound) || current.Status == "deleted" {
		if cleanupErr := s.index.DeleteDocument(ctx, p.Owner, p.SourceID); cleanupErr != nil {
			return finish("vector_cleanup_unavailable", cleanupErr)
		}
		return finish("obsolete", nil)
	}
	if err != nil {
		return finish("source_unavailable", err)
	}
	if SourceVersion(current) != p.Revision || current.Status != "ready" {
		return finish("obsolete", nil)
	}
	return finish("", nil)
}

func publishDocumentIndex(ctx context.Context, index VectorIndex, d Document, chunks []Chunk) error {
	if rollout, ok := index.(*RolloutIndex); ok {
		if err := publishDocumentIndex(ctx, rollout.Legacy, d, chunks); err != nil {
			return err
		}
		if err := publishDocumentIndex(ctx, rollout.Candidate, d, chunks); err != nil {
			rollout.CandidateFailures.Add(1)
			return err
		}
		return nil
	}
	if err := index.Ensure(ctx); err != nil {
		return err
	}
	return index.Upsert(ctx, d, chunks)
}
