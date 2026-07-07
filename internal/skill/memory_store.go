package skill

import (
	"context"
	"sort"
	"sync"
	"time"
)

type MemoryStore struct {
	mu         sync.RWMutex
	runs       map[string]Run
	createKeys map[string]string
	actionKeys map[string]string
	settings   map[string]bool
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{runs: make(map[string]Run), createKeys: make(map[string]string), actionKeys: make(map[string]string), settings: make(map[string]bool)}
}

func (s *MemoryStore) ListSkillSettings(_ context.Context, userID string) (map[string]bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]bool)
	prefix := userID + ":"
	for key, enabled := range s.settings {
		if len(key) > len(prefix) && key[:len(prefix)] == prefix {
			result[key[len(prefix):]] = enabled
		}
	}
	return result, nil
}

func (s *MemoryStore) SetSkillEnabled(_ context.Context, userID, skillName string, enabled bool, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings[userID+":"+skillName] = enabled
	return nil
}

func (s *MemoryStore) RecoverInterruptedSkillRuns(_ context.Context, now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	recovered := 0
	for runID, run := range s.runs {
		if run.Status != "running" || run.ExecutionMode == "worker" {
			continue
		}
		run.Status, run.CurrentState = "failed", "failed"
		run.ErrorCode = "execution_interrupted"
		run.ErrorMessage = "Skill execution was interrupted by a service restart; retry is safe"
		run.UpdatedAt, run.CompletedAt = now, &now
		run.Revision++
		s.runs[runID] = run
		recovered++
	}
	return recovered, nil
}

func (s *MemoryStore) ClaimSkillRun(_ context.Context, workerID string, now time.Time, lease time.Duration) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	selectedID := ""
	var selected Run
	for runID, run := range s.runs {
		available := run.ExecutionMode == "worker" && !run.AvailableAt.After(now) && (run.Status == "queued" || (run.Status == "running" && run.LeaseExpiresAt != nil && !run.LeaseExpiresAt.After(now)))
		if !available {
			continue
		}
		if selectedID == "" || run.AvailableAt.Before(selected.AvailableAt) || (run.AvailableAt.Equal(selected.AvailableAt) && run.CreatedAt.Before(selected.CreatedAt)) {
			selectedID, selected = runID, run
		}
	}
	if selectedID == "" {
		return Run{}, ErrNoQueuedRun
	}
	expires := now.Add(lease)
	selected.Status, selected.CurrentState = "running", "execute"
	selected.WorkerID, selected.LeaseExpiresAt = workerID, &expires
	selected.UpdatedAt = now
	selected.Revision++
	s.runs[selectedID] = cloneRun(selected)
	return cloneRun(selected), nil
}

func (s *MemoryStore) ClaimSkillRunByID(_ context.Context, runID, workerID string, now time.Time, lease time.Duration) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[runID]
	available := ok && run.ExecutionMode == "worker" && !run.AvailableAt.After(now) && (run.Status == "queued" || (run.Status == "running" && run.LeaseExpiresAt != nil && !run.LeaseExpiresAt.After(now)))
	if !available {
		return Run{}, ErrNoQueuedRun
	}
	expires := now.Add(lease)
	run.Status, run.CurrentState = "running", "execute"
	run.WorkerID, run.LeaseExpiresAt = workerID, &expires
	run.UpdatedAt, run.Revision = now, run.Revision+1
	s.runs[runID] = cloneRun(run)
	return cloneRun(run), nil
}

func (s *MemoryStore) RenewSkillRunLease(_ context.Context, runID, workerID string, revision int, now time.Time, lease time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[runID]
	if !ok || run.Status != "running" || run.ExecutionMode != "worker" || run.WorkerID != workerID || run.Revision != revision || run.LeaseExpiresAt == nil || !run.LeaseExpiresAt.After(now) {
		return ErrConflict
	}
	expires := now.Add(lease)
	run.LeaseExpiresAt = &expires
	run.UpdatedAt = now
	s.runs[runID] = cloneRun(run)
	return nil
}

func (s *MemoryStore) CreateSkillRun(_ context.Context, run Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	lookup := run.UserID + ":" + run.CreateKey
	if _, exists := s.createKeys[lookup]; exists {
		return ErrConflict
	}
	if _, exists := s.runs[run.ID]; exists {
		return ErrConflict
	}
	s.runs[run.ID] = cloneRun(run)
	s.createKeys[lookup] = run.ID
	return nil
}

func (s *MemoryStore) GetSkillRun(_ context.Context, userID, runID string) (Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	run, ok := s.runs[runID]
	if !ok || run.UserID != userID {
		return Run{}, ErrNotFound
	}
	return cloneRun(run), nil
}

func (s *MemoryStore) FindSkillRunByCreateKey(_ context.Context, userID, key string) (Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	runID, ok := s.createKeys[userID+":"+key]
	if !ok {
		return Run{}, ErrNotFound
	}
	return cloneRun(s.runs[runID]), nil
}

func (s *MemoryStore) ListSkillRuns(_ context.Context, userID string, limit int) ([]Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]Run, 0)
	for _, run := range s.runs {
		if run.UserID == userID {
			items = append(items, cloneRun(run))
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *MemoryStore) SaveSkillRun(_ context.Context, run Run, expectedRevision int, steps []Step, _ []GeneratedFile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.runs[run.ID]
	if !ok || current.UserID != run.UserID {
		return ErrNotFound
	}
	if current.Revision != expectedRevision || run.Revision != expectedRevision+1 {
		return ErrConflict
	}
	newActionKeys := make([]string, 0)
	for _, step := range steps {
		key := ""
		switch step.State {
		case "confirm":
			key = run.ConfirmationKey
		case "cancel", "retry":
			key = run.LastActionKey
		}
		if key == "" {
			continue
		}
		lookup := run.UserID + ":" + key
		if _, exists := s.actionKeys[lookup]; exists {
			return ErrConflict
		}
		newActionKeys = append(newActionKeys, lookup)
	}
	s.runs[run.ID] = cloneRun(run)
	for _, lookup := range newActionKeys {
		s.actionKeys[lookup] = run.ID
	}
	return nil
}

func cloneRun(run Run) Run {
	result := run
	result.Input = append([]byte(nil), run.Input...)
	result.Output = append([]byte(nil), run.Output...)
	result.Steps = make([]Step, len(run.Steps))
	copy(result.Steps, run.Steps)
	for index := range result.Steps {
		result.Steps[index].Input = append([]byte(nil), run.Steps[index].Input...)
		result.Steps[index].Output = append([]byte(nil), run.Steps[index].Output...)
	}
	result.Files = make([]GeneratedFile, len(run.Files))
	copy(result.Files, run.Files)
	if run.CompletedAt != nil {
		completed := *run.CompletedAt
		result.CompletedAt = &completed
	}
	if run.LeaseExpiresAt != nil {
		expires := *run.LeaseExpiresAt
		result.LeaseExpiresAt = &expires
	}
	return result
}
