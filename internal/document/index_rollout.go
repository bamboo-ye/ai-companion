package document

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/windcry1/ai-companion/internal/semantic"
	"sync/atomic"
	"time"
)

type RolloutIndex struct {
	Legacy, Candidate                                   VectorIndex
	Mode                                                string
	ShadowQueries, ShadowDifferences, CandidateFailures atomic.Int64
}

func NewKnowledgeIndex(url, collection, key string, timeout time.Duration, c semantic.Config) VectorIndex {
	legacy := NewQdrantIndex(url, collection, key, timeout)
	if c.EmbeddingModel == "" || c.IndexMode == "" || c.IndexMode == "legacy" {
		return legacy
	}
	client := semantic.New(c)
	version := fmt.Sprintf("%x", sha256.Sum256([]byte(client.Version())))[:12]
	candidate := NewQdrantIndex(url, collection+"_semantic_"+version, key, timeout)
	candidate.semantic = client
	return &RolloutIndex{Legacy: legacy, Candidate: candidate, Mode: c.IndexMode}
}
func (r *RolloutIndex) Ensure(ctx context.Context) error {
	if err := r.Legacy.Ensure(ctx); err != nil {
		return err
	}
	err := r.Candidate.Ensure(ctx)
	if err != nil {
		r.CandidateFailures.Add(1)
		if r.Mode == "semantic" {
			return err
		}
	}
	return nil
}
func (r *RolloutIndex) Upsert(ctx context.Context, d Document, chunks []Chunk) error {
	if err := r.Legacy.Upsert(ctx, d, chunks); err != nil {
		return err
	}
	err := r.Candidate.Upsert(ctx, d, chunks)
	if err != nil {
		r.CandidateFailures.Add(1)
		if r.Mode == "semantic" {
			return err
		}
	}
	return nil
}
func (r *RolloutIndex) Search(ctx context.Context, user, q string, ids []string, limit int) ([]SearchHit, error) {
	return r.search(ctx, q, ids, limit, func(index VectorIndex) ([]SearchHit, error) { return index.Search(ctx, user, q, ids, limit) })
}
func (r *RolloutIndex) SearchDocuments(ctx context.Context, q string, ids []string, limit int) ([]SearchHit, error) {
	return r.search(ctx, q, ids, limit, func(index VectorIndex) ([]SearchHit, error) { return index.SearchDocuments(ctx, q, ids, limit) })
}
func (r *RolloutIndex) search(ctx context.Context, _ string, _ []string, _ int, search func(VectorIndex) ([]SearchHit, error)) ([]SearchHit, error) {
	legacy, err := search(r.Legacy)
	candidate, candidateErr := search(r.Candidate)
	if candidateErr != nil {
		r.CandidateFailures.Add(1)
		return legacy, err
	}
	if r.Mode == "semantic" {
		if len(candidate) > 0 {
			return candidate, nil
		}
		return legacy, err
	}
	r.ShadowQueries.Add(1)
	if len(legacy) != len(candidate) || (len(legacy) > 0 && len(candidate) > 0 && legacy[0].ChunkID != candidate[0].ChunkID) {
		r.ShadowDifferences.Add(1)
	}
	return legacy, err
}
func (r *RolloutIndex) DeleteDocument(ctx context.Context, user, doc string) error {
	return errors.Join(r.Legacy.DeleteDocument(ctx, user, doc), r.Candidate.DeleteDocument(ctx, user, doc))
}

// Reindex is idempotent and leaves document/source versions unchanged.
func (s *Service) Reindex(ctx context.Context, user, doc string) error {
	d, err := s.Get(ctx, user, doc)
	if err != nil {
		return err
	}
	if d.Status != "ready" {
		return ErrValidation
	}
	parsed, ok := s.store.(ParsedStore)
	if !ok {
		return ErrValidation
	}
	chunks, err := parsed.ListDocumentChunks(ctx, user, doc, 100000)
	if err != nil {
		return err
	}
	if len(chunks) != d.ChunkCount {
		return ErrParsedContentUnavailable
	}
	if rollout, ok := s.index.(*RolloutIndex); ok {
		if err = rollout.Candidate.Ensure(ctx); err != nil {
			return err
		}
		if err = rollout.Candidate.Upsert(ctx, d, chunks); err != nil {
			rollout.CandidateFailures.Add(1)
			return err
		}
		return nil
	}
	if err = s.index.Ensure(ctx); err != nil {
		return err
	}
	return s.index.Upsert(ctx, d, chunks)
}
