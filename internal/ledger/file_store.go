package ledger

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type MemoryExportFileStore struct {
	mu    sync.RWMutex
	items map[string][]byte
}

func NewMemoryExportFileStore() *MemoryExportFileStore {
	return &MemoryExportFileStore{items: map[string][]byte{}}
}

func (s *MemoryExportFileStore) Put(_ context.Context, userID, exportID string, data []byte) (string, error) {
	key := userID + "/" + exportID + "/ledger.xlsx"
	s.mu.Lock()
	s.items[key] = append([]byte(nil), data...)
	s.mu.Unlock()
	return key, nil
}

func (s *MemoryExportFileStore) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, ok := s.items[key]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), data...), nil
}

type LocalExportFileStore struct{ root string }

func NewLocalExportFileStore(root string) (*LocalExportFileStore, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(absolute, 0o700); err != nil {
		return nil, err
	}
	return &LocalExportFileStore{root: absolute}, nil
}

func (s *LocalExportFileStore) Put(_ context.Context, userID, exportID string, data []byte) (string, error) {
	key := filepath.Join(userID, exportID, "ledger.xlsx")
	path, err := s.resolve(key)
	if err != nil {
		return "", err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	return key, os.WriteFile(path, data, 0o600)
}

func (s *LocalExportFileStore) Get(_ context.Context, key string) ([]byte, error) {
	path, err := s.resolve(key)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return data, err
}

func (s *LocalExportFileStore) resolve(key string) (string, error) {
	path := filepath.Clean(filepath.Join(s.root, key))
	relative, err := filepath.Rel(s.root, path)
	if err != nil || relative == ".." || filepath.IsAbs(relative) || len(relative) >= 3 && relative[:3] == ".."+string(filepath.Separator) {
		return "", fmt.Errorf("invalid ledger export storage key")
	}
	return path, nil
}
