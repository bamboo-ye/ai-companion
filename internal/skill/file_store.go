package skill

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type MemoryFileStore struct {
	mu    sync.RWMutex
	items map[string][]byte
}

func NewMemoryFileStore() *MemoryFileStore { return &MemoryFileStore{items: make(map[string][]byte)} }

func (s *MemoryFileStore) Put(_ context.Context, userID, runID, fileID string, data []byte) (string, error) {
	key := userID + "/" + runID + "/" + fileID
	s.mu.Lock()
	s.items[key] = append([]byte(nil), data...)
	s.mu.Unlock()
	return key, nil
}

func (s *MemoryFileStore) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, ok := s.items[key]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), data...), nil
}

func (s *MemoryFileStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	delete(s.items, key)
	s.mu.Unlock()
	return nil
}

type LocalFileStore struct{ root string }

func NewLocalFileStore(root string) (*LocalFileStore, error) {
	if root == "" {
		return nil, fmt.Errorf("skill file storage directory is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(absolute, 0o700); err != nil {
		return nil, err
	}
	return &LocalFileStore{root: absolute}, nil
}

func (s *LocalFileStore) Put(_ context.Context, userID, runID, fileID string, data []byte) (string, error) {
	key := filepath.Join(userID, runID, fileID)
	path, err := s.resolve(key)
	if err != nil {
		return "", err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err = os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return key, nil
}

func (s *LocalFileStore) Get(_ context.Context, key string) ([]byte, error) {
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

func (s *LocalFileStore) Delete(_ context.Context, key string) error {
	path, err := s.resolve(key)
	if err != nil {
		return err
	}
	if err = os.Remove(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *LocalFileStore) resolve(key string) (string, error) {
	path := filepath.Clean(filepath.Join(s.root, key))
	relative, err := filepath.Rel(s.root, path)
	if err != nil || relative == ".." || filepath.IsAbs(relative) || len(relative) >= 3 && relative[:3] == ".."+string(filepath.Separator) {
		return "", fmt.Errorf("invalid skill file storage key")
	}
	return path, nil
}
