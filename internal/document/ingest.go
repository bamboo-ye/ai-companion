package document

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

var ErrNoIngestJob = errors.New("no document ingest job available")

const EmbeddingVersion = "hashing-ngrams-256-v1"

type IngestJob struct {
	ID       string
	Document Document
	Attempts int
}

type Page struct {
	ID            string  `json:"id"`
	PageNo        int     `json:"page_no"`
	Text          string  `json:"text"`
	Quality       float64 `json:"quality"`
	ContentHash   string  `json:"content_hash"`
	ParserVersion string  `json:"parser_version"`
}

type Chunk struct {
	ID               string `json:"id"`
	PointID          string `json:"point_id"`
	Ordinal          int    `json:"ordinal"`
	PageStart        int    `json:"page_start"`
	PageEnd          int    `json:"page_end"`
	SectionPath      string `json:"section_path"`
	Content          string `json:"content"`
	TokenCount       int    `json:"token_count"`
	ContentHash      string `json:"content_hash"`
	ParserVersion    string `json:"parser_version"`
	EmbeddingVersion string `json:"embedding_version"`
}

type ParseResult struct {
	ParserVersion string  `json:"parser_version"`
	Pages         []Page  `json:"pages"`
	Chunks        []Chunk `json:"chunks"`
}

type Parser interface {
	Parse(context.Context, string, []byte) (ParseResult, error)
}

type IngestStore interface {
	ClaimIngestJob(context.Context, string, time.Time, time.Duration) (IngestJob, error)
	ClaimIngestJobByID(context.Context, string, string, time.Time, time.Duration) (IngestJob, error)
	SaveParsedDocument(context.Context, IngestJob, ParseResult, time.Time) error
	CompleteIngestJob(context.Context, string, time.Time) error
	FailIngestJob(context.Context, string, string, time.Time) error
}

type VectorIndex interface {
	Ensure(context.Context) error
	Upsert(context.Context, Document, []Chunk) error
	Search(context.Context, string, string, []string, int) ([]SearchHit, error)
	DeleteDocument(context.Context, string, string) error
}

type NoopVectorIndex struct{}

func (NoopVectorIndex) Ensure(context.Context) error                    { return nil }
func (NoopVectorIndex) Upsert(context.Context, Document, []Chunk) error { return nil }
func (NoopVectorIndex) Search(context.Context, string, string, []string, int) ([]SearchHit, error) {
	return nil, nil
}
func (NoopVectorIndex) DeleteDocument(context.Context, string, string) error { return nil }

type Ingestor struct {
	store    IngestStore
	blobs    BlobStore
	parser   Parser
	index    VectorIndex
	workerID string
	now      func() time.Time
}

func NewIngestor(store IngestStore, blobs BlobStore, parser Parser, index VectorIndex, workerID string) *Ingestor {
	return &Ingestor{store: store, blobs: blobs, parser: parser, index: index, workerID: workerID, now: time.Now}
}

func (i *Ingestor) RunOnce(ctx context.Context) (bool, error) {
	now := i.now().UTC()
	job, err := i.store.ClaimIngestJob(ctx, i.workerID, now, 2*time.Minute)
	if errors.Is(err, ErrNoIngestJob) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return i.process(ctx, job)
}

func (i *Ingestor) RunJob(ctx context.Context, jobID string) (bool, error) {
	job, err := i.store.ClaimIngestJobByID(ctx, jobID, i.workerID, i.now().UTC(), 2*time.Minute)
	if errors.Is(err, ErrNoIngestJob) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return i.process(ctx, job)
}

func (i *Ingestor) process(ctx context.Context, job IngestJob) (bool, error) {
	fail := func(cause error) (bool, error) {
		code := failureCode(cause)
		_ = i.store.FailIngestJob(ctx, job.ID, code, i.now().UTC())
		return true, cause
	}
	data, err := i.blobs.Get(ctx, job.Document.StorageKey)
	if err != nil {
		return fail(fmt.Errorf("read_blob: %w", err))
	}
	result, err := i.parser.Parse(ctx, job.Document.MediaType, data)
	if err != nil {
		return fail(fmt.Errorf("parse_document: %w", err))
	}
	prepareResult(job.Document.ID, &result)
	if err = i.index.Ensure(ctx); err != nil {
		return fail(fmt.Errorf("ensure_index: %w", err))
	}
	if err = i.index.Upsert(ctx, job.Document, result.Chunks); err != nil {
		return fail(fmt.Errorf("index_chunks: %w", err))
	}
	if err = i.store.SaveParsedDocument(ctx, job, result, i.now().UTC()); err != nil {
		return fail(fmt.Errorf("save_chunks: %w", err))
	}
	if err = i.store.CompleteIngestJob(ctx, job.ID, i.now().UTC()); err != nil {
		return true, err
	}
	return true, nil
}

func (i *Ingestor) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		processed, err := i.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			// The durable job state records the failure; continue serving later jobs.
		}
		if processed {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func prepareResult(documentID string, result *ParseResult) {
	for index := range result.Pages {
		result.Pages[index].ID = deterministicUUID(documentID + ":page:" + fmt.Sprint(result.Pages[index].PageNo) + ":" + result.ParserVersion)
		result.Pages[index].ParserVersion = result.ParserVersion
	}
	for index := range result.Chunks {
		seed := documentID + ":chunk:" + fmt.Sprint(result.Chunks[index].Ordinal) + ":" + result.ParserVersion
		result.Chunks[index].ID = deterministicUUID(seed)
		result.Chunks[index].PointID = deterministicUUID("point:" + seed)
		result.Chunks[index].ParserVersion = result.ParserVersion
		result.Chunks[index].EmbeddingVersion = EmbeddingVersion
	}
}

func deterministicUUID(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	b := sum[:16]
	b[6] = (b[6] & 0x0f) | 0x50
	b[8] = (b[8] & 0x3f) | 0x80
	hexValue := hex.EncodeToString(b)
	return hexValue[0:8] + "-" + hexValue[8:12] + "-" + hexValue[12:16] + "-" + hexValue[16:20] + "-" + hexValue[20:32]
}

func failureCode(err error) string {
	value := err.Error()
	if len(value) > 120 {
		value = value[:120]
	}
	return value
}
