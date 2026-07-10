package opsauth

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

type MemoryStore struct {
	mu       sync.RWMutex
	accounts map[string]Account
	byHash   map[string]string
	audits   []AuditInput
}

func NewMemoryStore(accounts ...Account) *MemoryStore {
	store := &MemoryStore{accounts: map[string]Account{}, byHash: map[string]string{}}
	for _, account := range accounts {
		store.Put(account)
	}
	return store
}

func (s *MemoryStore) Put(account Account) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if account.Status == "" {
		account.Status = "active"
	}
	s.accounts[account.ID] = account
	if account.TokenHash != "" {
		s.byHash[account.TokenHash] = account.ID
	}
}

func (s *MemoryStore) FindOperatorByTokenHash(_ context.Context, tokenHash string) (Account, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byHash[tokenHash]
	if !ok {
		return Account{}, ErrUnauthorized
	}
	account := s.accounts[id]
	return account, nil
}

func (s *MemoryStore) ListOperators(_ context.Context, filter OperatorFilter) ([]Account, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]Account, 0, len(s.accounts))
	for _, account := range s.accounts {
		if filter.Role != "" && account.Role != filter.Role {
			continue
		}
		if filter.Status != "" && account.Status != filter.Status {
			continue
		}
		items = append(items, account)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	if filter.Limit > 0 && len(items) > filter.Limit {
		items = items[:filter.Limit]
	}
	return items, nil
}

func (s *MemoryStore) GetOperator(_ context.Context, id string) (Account, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	account, ok := s.accounts[strings.TrimSpace(id)]
	if !ok {
		return Account{}, ErrNotFound
	}
	return account, nil
}

func (s *MemoryStore) CreateOperator(_ context.Context, account Account, audit AuditInput) (Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.accounts[account.ID]; exists {
		return Account{}, ErrConflict
	}
	if account.TokenHash != "" {
		if _, exists := s.byHash[account.TokenHash]; exists {
			return Account{}, ErrConflict
		}
	}
	s.accounts[account.ID] = account
	if account.TokenHash != "" {
		s.byHash[account.TokenHash] = account.ID
	}
	s.audits = append(s.audits, audit)
	return account, nil
}

func (s *MemoryStore) SetOperatorStatus(_ context.Context, id, status string, audit AuditInput) (Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	account, ok := s.accounts[strings.TrimSpace(id)]
	if !ok {
		return Account{}, ErrNotFound
	}
	account.Status = status
	account.UpdatedAt = audit.Now
	s.accounts[account.ID] = account
	s.audits = append(s.audits, audit)
	return account, nil
}

func (s *MemoryStore) ResetOperatorToken(_ context.Context, id, tokenHash string, audit AuditInput) (Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	account, ok := s.accounts[strings.TrimSpace(id)]
	if !ok {
		return Account{}, ErrNotFound
	}
	if owner, exists := s.byHash[tokenHash]; exists && owner != account.ID {
		return Account{}, ErrConflict
	}
	delete(s.byHash, account.TokenHash)
	account.TokenHash = tokenHash
	account.UpdatedAt = audit.Now
	s.accounts[account.ID] = account
	s.byHash[tokenHash] = account.ID
	s.audits = append(s.audits, audit)
	return account, nil
}

func (s *MemoryStore) ResetOperatorMFA(_ context.Context, id, secret string, enabled bool, audit AuditInput) (Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	account, ok := s.accounts[strings.TrimSpace(id)]
	if !ok {
		return Account{}, ErrNotFound
	}
	account.TOTPSecret = strings.TrimSpace(secret)
	account.MFAEnabled = enabled
	account.UpdatedAt = audit.Now
	s.accounts[account.ID] = account
	s.audits = append(s.audits, audit)
	return account, nil
}

func (s *MemoryStore) RecordOperatorAuthenticated(_ context.Context, id string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	account, ok := s.accounts[id]
	if !ok {
		return ErrUnauthorized
	}
	account.LastAuthenticatedAt = &now
	account.UpdatedAt = now
	s.accounts[id] = account
	return nil
}
