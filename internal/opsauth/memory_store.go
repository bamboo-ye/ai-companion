package opsauth

import (
	"context"
	"sync"
	"time"
)

type MemoryStore struct {
	mu       sync.RWMutex
	accounts map[string]Account
	byHash   map[string]string
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
