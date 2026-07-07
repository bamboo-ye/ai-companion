package document

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type LocalBlobStore struct {
	root string
}

func NewLocalBlobStore(root string) (*LocalBlobStore, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("document storage directory is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("create document storage directory: %w", err)
	}
	return &LocalBlobStore{root: absolute}, nil
}

func (s *LocalBlobStore) Put(ctx context.Context, key string, data []byte) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	path, err := s.path(key)
	if err != nil {
		return false, err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err = file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return false, err
	}
	if err = file.Close(); err != nil {
		_ = os.Remove(path)
		return false, err
	}
	return true, nil
}

func (s *LocalBlobStore) Get(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := s.path(key)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return data, err
}

func (s *LocalBlobStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := s.path(key)
	if err != nil {
		return err
	}
	if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *LocalBlobStore) path(key string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(strings.TrimSpace(key)))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid blob key")
	}
	full := filepath.Join(s.root, clean)
	if full != s.root && !strings.HasPrefix(full, s.root+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid blob key")
	}
	return full, nil
}
