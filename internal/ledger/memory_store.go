package ledger

import (
	"context"
	"sort"
	"sync"
	"time"
)

type MemoryStore struct {
	mu                sync.RWMutex
	candidates        map[string]Candidate
	entries           map[string]Entry
	idempotencyOwners map[string]string
	exports           map[string]ExportJob
	exportKeys        map[string]string
	workspaceShares   map[string]map[string]time.Time
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{candidates: map[string]Candidate{}, entries: map[string]Entry{}, idempotencyOwners: map[string]string{}, exports: map[string]ExportJob{}, exportKeys: map[string]string{}, workspaceShares: map[string]map[string]time.Time{}}
}

func (s *MemoryStore) CreateCandidate(_ context.Context, item Candidate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.candidates[item.ID] = item
	return nil
}

func (s *MemoryStore) GetCandidate(_ context.Context, userID, candidateID string) (Candidate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.candidates[candidateID]
	if !ok || item.UserID != userID {
		return Candidate{}, ErrNotFound
	}
	return item, nil
}

func (s *MemoryStore) ConfirmCandidate(_ context.Context, candidate Candidate, entry Entry, idempotencyKey string, now time.Time) (Entry, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := candidate.UserID + ":" + idempotencyKey
	if owner, ok := s.idempotencyOwners[key]; ok {
		item := s.entries[owner]
		if item.CandidateID != candidate.ID {
			return Entry{}, false, ErrIdempotencyReuse
		}
		return item, false, nil
	}
	stored, ok := s.candidates[candidate.ID]
	if !ok || stored.UserID != candidate.UserID {
		return Entry{}, false, ErrNotFound
	}
	if stored.Status == "confirmed" {
		for _, existing := range s.entries {
			if existing.CandidateID == candidate.ID && existing.Status == "active" {
				return existing, false, nil
			}
		}
	}
	stored.Status = "confirmed"
	stored.UpdatedAt = now
	s.candidates[stored.ID] = stored
	s.entries[entry.ID] = entry
	s.idempotencyOwners[key] = entry.ID
	return entry, true, nil
}

func (s *MemoryStore) ListEntries(_ context.Context, userID string, filter EntryFilter) ([]Entry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Entry, 0)
	for _, item := range s.entries {
		if item.UserID != userID || item.Status != "active" {
			continue
		}
		if filter.Start != nil && item.OccurredAt.Before(*filter.Start) {
			continue
		}
		if filter.End != nil && !item.OccurredAt.Before(*filter.End) {
			continue
		}
		if filter.Direction != "" && item.Direction != filter.Direction {
			continue
		}
		if filter.Category != "" && item.Category != filter.Category {
			continue
		}
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].OccurredAt.Equal(result[j].OccurredAt) {
			return result[i].CreatedAt.After(result[j].CreatedAt)
		}
		return result[i].OccurredAt.After(result[j].OccurredAt)
	})
	if len(result) > filter.Limit {
		result = result[:filter.Limit]
	}
	return result, nil
}

func (s *MemoryStore) GetEntry(_ context.Context, userID, entryID string) (Entry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.entries[entryID]
	if !ok || item.UserID != userID || item.Status != "active" {
		return Entry{}, ErrNotFound
	}
	return item, nil
}

func (s *MemoryStore) UpdateEntry(_ context.Context, item Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.entries[item.ID]
	if !ok || existing.UserID != item.UserID || existing.Status != "active" {
		return ErrNotFound
	}
	s.entries[item.ID] = item
	return nil
}

func (s *MemoryStore) DeleteEntry(_ context.Context, userID, entryID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.entries[entryID]
	if !ok || item.UserID != userID || item.Status != "active" {
		return ErrNotFound
	}
	item.Status = "deleted"
	item.DeletedAt = &now
	item.UpdatedAt = now
	s.entries[entryID] = item
	return nil
}

func (s *MemoryStore) CreateExport(_ context.Context, job ExportJob, requestKey string) (ExportJob, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := job.UserID + ":" + requestKey
	if existingID := s.exportKeys[key]; existingID != "" {
		existing := s.exports[existingID]
		if existing.Month != job.Month || existing.Currency != job.Currency || existing.Timezone != job.Timezone {
			return ExportJob{}, false, ErrIdempotencyReuse
		}
		return existing, false, nil
	}
	s.exports[job.ID] = job
	s.exportKeys[key] = job.ID
	return job, true, nil
}

func (s *MemoryStore) GetExport(_ context.Context, userID, exportID string) (ExportJob, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	job, ok := s.exports[exportID]
	if !ok || job.UserID != userID {
		return ExportJob{}, ErrNotFound
	}
	return job, nil
}

func (s *MemoryStore) ClaimExportByID(_ context.Context, exportID, workerID string, now time.Time, lease time.Duration) (ExportJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.exports[exportID]
	if !ok || job.Status != "queued" {
		return ExportJob{}, ErrNotFound
	}
	job.Status = "processing"
	job.UpdatedAt = now
	s.exports[exportID] = job
	return job, nil
}

func (s *MemoryStore) ClaimExport(_ context.Context, workerID string, now time.Time, lease time.Duration) (ExportJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, job := range s.exports {
		if job.Status != "queued" {
			continue
		}
		job.Status, job.WorkerID, job.UpdatedAt = "processing", workerID, now
		s.exports[id] = job
		return job, nil
	}
	return ExportJob{}, ErrNotFound
}

func (s *MemoryStore) CompleteExport(_ context.Context, job ExportJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.exports[job.ID]; !ok {
		return ErrNotFound
	}
	s.exports[job.ID] = job
	return nil
}

func (s *MemoryStore) FailExport(_ context.Context, exportID, _ string, code string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.exports[exportID]
	if !ok {
		return ErrNotFound
	}
	job.Status, job.FailureCode, job.UpdatedAt, job.CompletedAt = "failed", code, now, &now
	s.exports[exportID] = job
	return nil
}

func (s *MemoryStore) ShareExportWithWorkspace(_ context.Context, userID, workspaceID, exportID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.exports[exportID]
	if !ok || job.UserID != userID || job.Status != "completed" || job.StorageKey == "" {
		return ErrNotFound
	}
	if s.workspaceShares == nil {
		s.workspaceShares = map[string]map[string]time.Time{}
	}
	if s.workspaceShares[workspaceID] == nil {
		s.workspaceShares[workspaceID] = map[string]time.Time{}
	}
	s.workspaceShares[workspaceID][exportID] = now
	return nil
}

func (s *MemoryStore) ListWorkspaceExports(_ context.Context, workspaceID string, limit int) ([]ExportJob, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]ExportJob, 0)
	for exportID := range s.workspaceShares[workspaceID] {
		job, ok := s.exports[exportID]
		if ok && job.Status == "completed" && job.StorageKey != "" {
			items = append(items, job)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *MemoryStore) GetWorkspaceExport(_ context.Context, workspaceID, exportID string) (ExportJob, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.workspaceShares[workspaceID][exportID]; !ok {
		return ExportJob{}, ErrNotFound
	}
	job, ok := s.exports[exportID]
	if !ok || job.Status != "completed" || job.StorageKey == "" {
		return ExportJob{}, ErrNotFound
	}
	return job, nil
}
