package document

import (
	"context"
	"sort"
	"sync"
	"time"
)

type MemoryStore struct {
	mu              sync.RWMutex
	items           map[string]Document
	jobs            map[string]memoryJob
	pages           map[string][]Page
	chunks          map[string][]Chunk
	workspaceShares map[string]map[string]time.Time
}

type memoryJob struct {
	job          IngestJob
	status       string
	leaseExpires time.Time
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{items: map[string]Document{}, jobs: map[string]memoryJob{}, pages: map[string][]Page{}, chunks: map[string][]Chunk{}, workspaceShares: map[string]map[string]time.Time{}}
}

func (s *MemoryStore) CreateDocument(_ context.Context, item Document) (Document, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.items {
		if existing.UserID == item.UserID && existing.SHA256 == item.SHA256 && existing.Status != "deleted" {
			return existing, false, nil
		}
	}
	s.items[item.ID] = item
	s.jobs[item.JobID] = memoryJob{job: IngestJob{ID: item.JobID, Document: item}, status: "queued"}
	return item, true, nil
}

func (s *MemoryStore) ListDocuments(_ context.Context, userID string, limit int) ([]Document, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]Document, 0)
	for _, item := range s.items {
		if item.UserID == userID && item.Status != "deleted" {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *MemoryStore) GetDocument(_ context.Context, userID, documentID string) (Document, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.items[documentID]
	if !ok || item.UserID != userID || item.Status == "deleted" {
		return Document{}, ErrNotFound
	}
	return item, nil
}

func (s *MemoryStore) ListDocumentChunks(
	_ context.Context,
	userID string,
	documentID string,
	limit int,
) ([]Chunk, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.items[documentID]
	if !ok || item.UserID != userID || item.Status == "deleted" {
		return nil, ErrNotFound
	}
	chunks := append([]Chunk(nil), s.chunks[documentID]...)
	sort.Slice(chunks, func(i, j int) bool { return chunks[i].Ordinal < chunks[j].Ordinal })
	if limit > 0 && len(chunks) > limit {
		chunks = chunks[:limit]
	}
	return chunks, nil
}

func (s *MemoryStore) DeleteDocument(_ context.Context, userID, documentID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[documentID]
	if !ok || item.UserID != userID || item.Status == "deleted" {
		return ErrNotFound
	}
	item.Status = "deleted"
	item.DeletedAt = &now
	item.UpdatedAt = now
	s.items[documentID] = item
	for jobID, state := range s.jobs {
		if state.job.Document.ID == documentID && (state.status == "queued" || state.status == "processing") {
			state.status = "cancelled"
			s.jobs[jobID] = state
		}
	}
	return nil
}

func (s *MemoryStore) ShareDocumentWithWorkspace(_ context.Context, userID, workspaceID, documentID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[documentID]
	if !ok || item.UserID != userID || item.Status == "deleted" {
		return ErrNotFound
	}
	if s.workspaceShares == nil {
		s.workspaceShares = map[string]map[string]time.Time{}
	}
	if s.workspaceShares[workspaceID] == nil {
		s.workspaceShares[workspaceID] = map[string]time.Time{}
	}
	s.workspaceShares[workspaceID][documentID] = now
	return nil
}

func (s *MemoryStore) ListWorkspaceDocuments(_ context.Context, workspaceID string, limit int) ([]Document, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]Document, 0)
	for documentID := range s.workspaceShares[workspaceID] {
		item, ok := s.items[documentID]
		if ok && item.Status != "deleted" {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *MemoryStore) ClaimIngestJob(_ context.Context, _ string, now time.Time, lease time.Duration) (IngestJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var selectedID string
	var selected memoryJob
	for jobID, state := range s.jobs {
		available := state.status == "queued" || (state.status == "processing" && !state.leaseExpires.After(now))
		if !available || state.job.Document.Status == "deleted" {
			continue
		}
		if selectedID == "" || state.job.Document.CreatedAt.Before(selected.job.Document.CreatedAt) {
			selectedID, selected = jobID, state
		}
	}
	if selectedID == "" {
		return IngestJob{}, ErrNoIngestJob
	}
	selected.status = "processing"
	selected.leaseExpires = now.Add(lease)
	selected.job.Attempts++
	document := s.items[selected.job.Document.ID]
	document.Status = "processing"
	document.UpdatedAt = now
	s.items[document.ID] = document
	selected.job.Document = document
	s.jobs[selectedID] = selected
	return selected.job, nil
}

func (s *MemoryStore) ClaimIngestJobByID(_ context.Context, jobID, _ string, now time.Time, lease time.Duration) (IngestJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.jobs[jobID]
	available := ok && (state.status == "queued" || (state.status == "processing" && !state.leaseExpires.After(now)))
	if !available || state.job.Document.Status == "deleted" {
		return IngestJob{}, ErrNoIngestJob
	}
	state.status, state.leaseExpires = "processing", now.Add(lease)
	state.job.Attempts++
	document := s.items[state.job.Document.ID]
	document.Status, document.UpdatedAt = "processing", now
	s.items[document.ID] = document
	state.job.Document = document
	s.jobs[jobID] = state
	return state.job, nil
}

func (s *MemoryStore) SaveParsedDocument(_ context.Context, job IngestJob, result ParseResult, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[job.Document.ID]
	if !ok || item.Status == "deleted" {
		return ErrNotFound
	}
	s.pages[item.ID] = append([]Page(nil), result.Pages...)
	s.chunks[item.ID] = append([]Chunk(nil), result.Chunks...)
	item.ParserVersion = result.ParserVersion
	item.PageCount = len(result.Pages)
	item.ChunkCount = len(result.Chunks)
	item.Status = "processing"
	item.UpdatedAt = now
	s.items[item.ID] = item
	return nil
}

func (s *MemoryStore) CompleteIngestJob(_ context.Context, jobID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.jobs[jobID]
	if !ok || state.status != "processing" {
		return ErrNotFound
	}
	state.status = "completed"
	s.jobs[jobID] = state
	item := s.items[state.job.Document.ID]
	item.Status = "ready"
	item.UpdatedAt = now
	s.items[item.ID] = item
	return nil
}

func (s *MemoryStore) FailIngestJob(_ context.Context, jobID, code string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.jobs[jobID]
	if !ok {
		return ErrNotFound
	}
	state.status = "failed"
	s.jobs[jobID] = state
	item := s.items[state.job.Document.ID]
	item.Status = "failed"
	item.FailureCode = code
	item.UpdatedAt = now
	s.items[item.ID] = item
	return nil
}

type MemoryBlobStore struct {
	mu    sync.RWMutex
	items map[string][]byte
}

func NewMemoryBlobStore() *MemoryBlobStore { return &MemoryBlobStore{items: map[string][]byte{}} }

func (s *MemoryBlobStore) Put(_ context.Context, key string, data []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[key]; ok {
		return false, nil
	}
	s.items[key] = append([]byte(nil), data...)
	return true, nil
}

func (s *MemoryBlobStore) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, ok := s.items[key]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), data...), nil
}

func (s *MemoryBlobStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, key)
	return nil
}

func (s *MemoryBlobStore) Exists(key string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.items[key]
	return ok
}
