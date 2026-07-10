package document

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/windcry1/ai-companion/internal/platform/id"
)

var (
	ErrNotFound        = errors.New("document not found")
	ErrValidation      = errors.New("document validation failed")
	ErrUnsupportedType = errors.New("unsupported document type")
	ErrUnsafeContent   = errors.New("unsafe document content")
)

const defaultMaxUploadBytes int64 = 20 << 20

type Document struct {
	ID            string     `json:"id"`
	UserID        string     `json:"-"`
	FileID        string     `json:"-"`
	JobID         string     `json:"job_id"`
	Name          string     `json:"name"`
	MediaType     string     `json:"media_type"`
	SizeBytes     int64      `json:"size_bytes"`
	SHA256        string     `json:"sha256"`
	StorageKey    string     `json:"-"`
	Status        string     `json:"status"`
	ParserVersion string     `json:"parser_version,omitempty"`
	PageCount     int        `json:"page_count"`
	ChunkCount    int        `json:"chunk_count"`
	FailureCode   string     `json:"failure_code,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	DeletedAt     *time.Time `json:"deleted_at,omitempty"`
}

type Store interface {
	CreateDocument(context.Context, Document) (Document, bool, error)
	ListDocuments(context.Context, string, int) ([]Document, error)
	GetDocument(context.Context, string, string) (Document, error)
	DeleteDocument(context.Context, string, string, time.Time) error
	ShareDocumentWithWorkspace(context.Context, string, string, string, time.Time) error
	ListWorkspaceDocuments(context.Context, string, int) ([]Document, error)
}

type BlobStore interface {
	Put(context.Context, string, []byte) (bool, error)
	Get(context.Context, string) ([]byte, error)
	Delete(context.Context, string) error
}

type Service struct {
	store    Store
	blobs    BlobStore
	maxBytes int64
	now      func() time.Time
	index    VectorIndex
}

func NewService(store Store, blobs BlobStore, maxBytes int64) *Service {
	if maxBytes <= 0 {
		maxBytes = defaultMaxUploadBytes
	}
	return &Service{store: store, blobs: blobs, maxBytes: maxBytes, now: time.Now, index: NoopVectorIndex{}}
}

func (s *Service) SetVectorIndex(index VectorIndex) {
	if index != nil {
		s.index = index
	}
}

func (s *Service) MaxUploadBytes() int64 { return s.maxBytes }

func (s *Service) Upload(ctx context.Context, userID, originalName string, data []byte) (Document, bool, error) {
	name, err := cleanName(originalName)
	if err != nil {
		return Document{}, false, err
	}
	if len(data) == 0 || int64(len(data)) > s.maxBytes {
		return Document{}, false, fmt.Errorf("%w: file size must be between 1 and %d bytes", ErrValidation, s.maxBytes)
	}
	mediaType, err := detectMediaType(data)
	if err != nil {
		return Document{}, false, err
	}
	if bytes.Contains(data, []byte("EICAR-STANDARD-ANTIVIRUS-TEST-FILE")) {
		return Document{}, false, ErrUnsafeContent
	}
	digest := sha256.Sum256(data)
	hash := hex.EncodeToString(digest[:])
	fileID, err := id.New()
	if err != nil {
		return Document{}, false, err
	}
	documentID, err := id.New()
	if err != nil {
		return Document{}, false, err
	}
	jobID, err := id.New()
	if err != nil {
		return Document{}, false, err
	}
	now := s.now().UTC()
	item := Document{
		ID: documentID, UserID: userID, FileID: fileID, JobID: jobID, Name: name,
		MediaType: mediaType, SizeBytes: int64(len(data)), SHA256: hash,
		StorageKey: storageKey(userID, hash), Status: "queued", CreatedAt: now, UpdatedAt: now,
	}
	_, err = s.blobs.Put(ctx, item.StorageKey, data)
	if err != nil {
		return Document{}, false, fmt.Errorf("store document blob: %w", err)
	}
	saved, created, err := s.store.CreateDocument(ctx, item)
	if err != nil {
		// Content-addressed blobs are intentionally retained when metadata creation fails.
		// A later retry reuses the same key; periodic orphan cleanup can remove unreferenced blobs.
		return Document{}, false, err
	}
	return saved, created, nil
}

func (s *Service) List(ctx context.Context, userID string) ([]Document, error) {
	return s.store.ListDocuments(ctx, userID, 200)
}

func (s *Service) Get(ctx context.Context, userID, documentID string) (Document, error) {
	return s.store.GetDocument(ctx, userID, documentID)
}

func (s *Service) ShareWithWorkspace(ctx context.Context, userID, workspaceID, documentID string) error {
	workspaceID = strings.TrimSpace(workspaceID)
	documentID = strings.TrimSpace(documentID)
	if workspaceID == "" || documentID == "" {
		return fmt.Errorf("%w: workspace_id and document_id are required", ErrValidation)
	}
	return s.store.ShareDocumentWithWorkspace(ctx, userID, workspaceID, documentID, s.now().UTC())
}

func (s *Service) ListWorkspace(ctx context.Context, workspaceID string) ([]Document, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return nil, fmt.Errorf("%w: workspace_id is required", ErrValidation)
	}
	return s.store.ListWorkspaceDocuments(ctx, workspaceID, 200)
}

func (s *Service) Delete(ctx context.Context, userID, documentID string) error {
	item, err := s.store.GetDocument(ctx, userID, documentID)
	if err != nil {
		return err
	}
	if err = s.store.DeleteDocument(ctx, userID, documentID, s.now().UTC()); err != nil {
		return err
	}
	if durable, ok := s.store.(interface{ DurableCleanupDispatch() bool }); ok && durable.DurableCleanupDispatch() {
		return nil
	}
	// MySQL is the visibility authority. External cleanup is best-effort; retrieval
	// rechecks active ownership so stale points can never resurrect a deleted document.
	_ = s.index.DeleteDocument(ctx, userID, documentID)
	_ = s.blobs.Delete(ctx, item.StorageKey)
	return nil
}

func cleanName(value string) (string, error) {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	name := filepath.Base(value)
	if name == "" || name == "." || name == "/" || len([]rune(name)) > 255 {
		return "", fmt.Errorf("%w: invalid file name", ErrValidation)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%w: invalid file name", ErrValidation)
		}
	}
	return name, nil
}

func detectMediaType(data []byte) (string, error) {
	header := data
	if len(header) > 1024 {
		header = header[:1024]
	}
	if bytes.Contains(header, []byte("%PDF-")) {
		return "application/pdf", nil
	}
	if utf8.Valid(data) && !bytes.ContainsRune(data, '\x00') {
		return "text/plain", nil
	}
	return "", ErrUnsupportedType
}

func storageKey(userID, hash string) string {
	return "users/" + userID + "/documents/sha256/" + hash
}
