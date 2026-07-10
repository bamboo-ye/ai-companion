package mysqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/document"
	"github.com/windcry1/ai-companion/internal/platform/id"
)

func (s *Store) CreateDocument(ctx context.Context, item document.Document) (document.Document, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return item, false, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO files (id,user_id,original_name,media_type,size_bytes,sha256,storage_key,status,created_at,updated_at) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),?,?,?,?,?,'active',?,?) ON DUPLICATE KEY UPDATE id=id`, item.FileID, item.UserID, item.Name, item.MediaType, item.SizeBytes, item.SHA256, item.StorageKey, item.CreatedAt, item.UpdatedAt); err != nil {
		return item, false, err
	}
	var storedFileID string
	if err = tx.QueryRowContext(ctx, `SELECT BIN_TO_UUID(id) FROM files WHERE user_id=UUID_TO_BIN(?) AND active_sha256=?`, item.UserID, item.SHA256).Scan(&storedFileID); err != nil {
		return item, false, err
	}
	if storedFileID != item.FileID {
		existing, scanErr := scanDocument(tx.QueryRowContext(ctx, documentSelect+` WHERE f.id=UUID_TO_BIN(?) AND d.ingest_status<>'deleted'`, storedFileID))
		return existing, false, scanErr
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO documents (id,user_id,file_id,ingest_status,page_count,chunk_count,created_at,updated_at) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),UUID_TO_BIN(?),?,0,0,?,?)`, item.ID, item.UserID, item.FileID, item.Status, item.CreatedAt, item.UpdatedAt); err != nil {
		return item, false, err
	}
	idempotencyKey := "document.ingest.v1:" + item.ID
	if _, err = tx.ExecContext(ctx, `INSERT INTO document_ingest_jobs (id,document_id,user_id,status,idempotency_key,available_at,created_at,updated_at) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),UUID_TO_BIN(?),'queued',?,?,?,?)`, item.JobID, item.ID, item.UserID, idempotencyKey, item.CreatedAt, item.CreatedAt, item.UpdatedAt); err != nil {
		return item, false, err
	}
	eventID, err := id.New()
	if err != nil {
		return item, false, err
	}
	payload, err := json.Marshal(map[string]string{"document_id": item.ID, "job_id": item.JobID, "user_id": item.UserID})
	if err != nil {
		return item, false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO outbox_events (id,aggregate_type,aggregate_id,event_type,event_version,payload,occurred_at) VALUES (UUID_TO_BIN(?),'document',UUID_TO_BIN(?),'document.ingest.v1',1,?,?)`, eventID, item.ID, payload, item.CreatedAt); err != nil {
		return item, false, err
	}
	return item, true, tx.Commit()
}

func (s *Store) ListDocuments(ctx context.Context, userID string, limit int) ([]document.Document, error) {
	rows, err := s.db.QueryContext(ctx, documentSelect+` WHERE d.user_id=UUID_TO_BIN(?) AND d.ingest_status<>'deleted' AND f.status='active' ORDER BY d.created_at DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]document.Document, 0)
	for rows.Next() {
		item, err := scanDocument(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetDocument(ctx context.Context, userID, documentID string) (document.Document, error) {
	item, err := scanDocument(s.db.QueryRowContext(ctx, documentSelect+` WHERE d.id=UUID_TO_BIN(?) AND d.user_id=UUID_TO_BIN(?) AND d.ingest_status<>'deleted' AND f.status='active'`, documentID, userID))
	if errors.Is(err, sql.ErrNoRows) {
		err = document.ErrNotFound
	}
	return item, err
}

func (s *Store) DeleteDocument(ctx context.Context, userID, documentID string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var fileID, storageKey string
	if err = tx.QueryRowContext(ctx, `SELECT BIN_TO_UUID(d.file_id),f.storage_key FROM documents d JOIN files f ON f.id=d.file_id WHERE d.id=UUID_TO_BIN(?) AND d.user_id=UUID_TO_BIN(?) AND d.ingest_status<>'deleted' FOR UPDATE`, documentID, userID).Scan(&fileID, &storageKey); errors.Is(err, sql.ErrNoRows) {
		return document.ErrNotFound
	} else if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE documents SET ingest_status='deleted',deleted_at=?,updated_at=? WHERE id=UUID_TO_BIN(?)`, now, now, documentID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE files SET status='deleted',deleted_at=?,updated_at=? WHERE id=UUID_TO_BIN(?)`, now, now, fileID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE document_ingest_jobs SET status='cancelled',finished_at=?,updated_at=? WHERE document_id=UUID_TO_BIN(?) AND status IN ('queued','processing')`, now, now, documentID); err != nil {
		return err
	}
	cleanupID, err := id.New()
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO document_cleanup_jobs (id,document_id,user_id,storage_key,status,available_at,created_at,updated_at) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),UUID_TO_BIN(?),?,'queued',?,?,?)`, cleanupID, documentID, userID, storageKey, now, now, now); err != nil {
		return err
	}
	eventID, err := id.New()
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"job_id": cleanupID, "document_id": documentID, "user_id": userID})
	if _, err = tx.ExecContext(ctx, `INSERT INTO outbox_events (id,aggregate_type,aggregate_id,event_type,event_version,payload,occurred_at) VALUES (UUID_TO_BIN(?),'document',UUID_TO_BIN(?),'document.cleanup.v1',1,?,?)`, eventID, documentID, payload, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ShareDocumentWithWorkspace(ctx context.Context, userID, workspaceID, documentID string, now time.Time) error {
	if _, err := s.GetDocument(ctx, userID, documentID); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO workspace_document_shares (workspace_id,document_id,shared_by,created_at) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),UUID_TO_BIN(?),?) ON DUPLICATE KEY UPDATE created_at=created_at`, workspaceID, documentID, userID, now); err != nil {
		return err
	}
	return nil
}

func (s *Store) ListWorkspaceDocuments(ctx context.Context, workspaceID string, limit int) ([]document.Document, error) {
	rows, err := s.db.QueryContext(ctx, documentSelect+` JOIN workspace_document_shares wds ON wds.document_id=d.id WHERE wds.workspace_id=UUID_TO_BIN(?) AND d.ingest_status<>'deleted' AND f.status='active' ORDER BY wds.created_at DESC,d.created_at DESC LIMIT ?`, workspaceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]document.Document, 0)
	for rows.Next() {
		item, err := scanDocument(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) DurableCleanupDispatch() bool { return true }

func (s *Store) ClaimCleanupJobByID(ctx context.Context, jobID, workerID string, now time.Time, lease time.Duration) (document.CleanupJob, error) {
	return s.claimCleanupJob(ctx, `id=UUID_TO_BIN(?)`, []any{jobID}, workerID, now, lease)
}

func (s *Store) ClaimCleanupJob(ctx context.Context, workerID string, now time.Time, lease time.Duration) (document.CleanupJob, error) {
	return s.claimCleanupJob(ctx, `1=1`, nil, workerID, now, lease)
}

func (s *Store) claimCleanupJob(ctx context.Context, predicate string, args []any, workerID string, now time.Time, lease time.Duration) (document.CleanupJob, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return document.CleanupJob{}, err
	}
	defer tx.Rollback()
	queryArgs := append(append([]any{}, args...), now, now)
	query := `SELECT BIN_TO_UUID(id),BIN_TO_UUID(document_id),BIN_TO_UUID(user_id),storage_key,attempts FROM document_cleanup_jobs WHERE ` + predicate + ` AND available_at<=? AND (status='queued' OR (status='processing' AND lease_expires_at<=?)) ORDER BY available_at,created_at LIMIT 1 FOR UPDATE SKIP LOCKED`
	var job document.CleanupJob
	if err = tx.QueryRowContext(ctx, query, queryArgs...).Scan(&job.ID, &job.DocumentID, &job.UserID, &job.StorageKey, &job.Attempts); errors.Is(err, sql.ErrNoRows) {
		return document.CleanupJob{}, document.ErrNoCleanupJob
	} else if err != nil {
		return document.CleanupJob{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE document_cleanup_jobs SET status='processing',attempts=attempts+1,worker_id=?,lease_expires_at=?,last_error=NULL,updated_at=? WHERE id=UUID_TO_BIN(?)`, workerID, now.Add(lease), now, job.ID); err != nil {
		return document.CleanupJob{}, err
	}
	if err = tx.Commit(); err != nil {
		return document.CleanupJob{}, err
	}
	job.Attempts++
	return job, nil
}

func (s *Store) CompleteCleanupJob(ctx context.Context, job document.CleanupJob, workerID string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE document_cleanup_jobs SET status='completed',worker_id=NULL,lease_expires_at=NULL,last_error=NULL,updated_at=?,completed_at=? WHERE id=UUID_TO_BIN(?) AND status='processing' AND worker_id=?`, now, now, job.ID, workerID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return document.ErrNoCleanupJob
	}
	return nil
}

func (s *Store) FailCleanupJob(ctx context.Context, job document.CleanupJob, workerID string, cause error, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	message := strings.TrimSpace(cause.Error())
	if len(message) > 1024 {
		message = message[:1024]
	}
	delay := time.Second * time.Duration(1<<min(job.Attempts, 8))
	retryAt := now.Add(delay)
	result, err := tx.ExecContext(ctx, `UPDATE document_cleanup_jobs SET status='queued',available_at=?,worker_id=NULL,lease_expires_at=NULL,last_error=?,updated_at=? WHERE id=UUID_TO_BIN(?) AND status='processing' AND worker_id=?`, retryAt, message, now, job.ID, workerID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return document.ErrNoCleanupJob
	}
	eventID, err := id.New()
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"job_id": job.ID, "document_id": job.DocumentID, "user_id": job.UserID})
	if _, err = tx.ExecContext(ctx, `INSERT INTO outbox_events (id,aggregate_type,aggregate_id,event_type,event_version,payload,occurred_at,status,available_at) VALUES (UUID_TO_BIN(?),'document',UUID_TO_BIN(?),'document.cleanup.v1',1,?,?,'pending',?)`, eventID, job.DocumentID, payload, now, retryAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ClaimIngestJob(ctx context.Context, workerID string, now time.Time, lease time.Duration) (document.IngestJob, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return document.IngestJob{}, err
	}
	defer tx.Rollback()
	var jobID string
	err = tx.QueryRowContext(ctx, `SELECT BIN_TO_UUID(j.id) FROM document_ingest_jobs j JOIN documents d ON d.id=j.document_id WHERE d.ingest_status<>'deleted' AND j.available_at<=? AND (j.status='queued' OR (j.status='processing' AND j.lease_expires_at<=?)) ORDER BY j.available_at,j.created_at LIMIT 1 FOR UPDATE SKIP LOCKED`, now, now).Scan(&jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return document.IngestJob{}, document.ErrNoIngestJob
	}
	if err != nil {
		return document.IngestJob{}, err
	}
	leaseExpires := now.Add(lease)
	if _, err = tx.ExecContext(ctx, `UPDATE document_ingest_jobs SET status='processing',attempts=attempts+1,worker_id=?,lease_expires_at=?,started_at=COALESCE(started_at,?),updated_at=? WHERE id=UUID_TO_BIN(?)`, workerID, leaseExpires, now, now, jobID); err != nil {
		return document.IngestJob{}, err
	}
	item, err := scanDocument(tx.QueryRowContext(ctx, documentSelect+` WHERE j.id=UUID_TO_BIN(?)`, jobID))
	if err != nil {
		return document.IngestJob{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE documents SET ingest_status='processing',failure_code=NULL,updated_at=? WHERE id=UUID_TO_BIN(?)`, now, item.ID); err != nil {
		return document.IngestJob{}, err
	}
	var attempts int
	if err = tx.QueryRowContext(ctx, `SELECT attempts FROM document_ingest_jobs WHERE id=UUID_TO_BIN(?)`, jobID).Scan(&attempts); err != nil {
		return document.IngestJob{}, err
	}
	if err = tx.Commit(); err != nil {
		return document.IngestJob{}, err
	}
	item.Status = "processing"
	return document.IngestJob{ID: jobID, Document: item, Attempts: attempts}, nil
}

func (s *Store) ClaimIngestJobByID(ctx context.Context, jobID, workerID string, now time.Time, lease time.Duration) (document.IngestJob, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return document.IngestJob{}, err
	}
	defer tx.Rollback()
	var selectedID string
	err = tx.QueryRowContext(ctx, `SELECT BIN_TO_UUID(j.id) FROM document_ingest_jobs j JOIN documents d ON d.id=j.document_id WHERE j.id=UUID_TO_BIN(?) AND d.ingest_status<>'deleted' AND j.available_at<=? AND (j.status='queued' OR (j.status='processing' AND j.lease_expires_at<=?)) FOR UPDATE`, jobID, now, now).Scan(&selectedID)
	if errors.Is(err, sql.ErrNoRows) {
		return document.IngestJob{}, document.ErrNoIngestJob
	}
	if err != nil {
		return document.IngestJob{}, err
	}
	leaseExpires := now.Add(lease)
	if _, err = tx.ExecContext(ctx, `UPDATE document_ingest_jobs SET status='processing',attempts=attempts+1,worker_id=?,lease_expires_at=?,started_at=COALESCE(started_at,?),updated_at=? WHERE id=UUID_TO_BIN(?)`, workerID, leaseExpires, now, now, selectedID); err != nil {
		return document.IngestJob{}, err
	}
	item, err := scanDocument(tx.QueryRowContext(ctx, documentSelect+` WHERE j.id=UUID_TO_BIN(?)`, selectedID))
	if err != nil {
		return document.IngestJob{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE documents SET ingest_status='processing',failure_code=NULL,updated_at=? WHERE id=UUID_TO_BIN(?)`, now, item.ID); err != nil {
		return document.IngestJob{}, err
	}
	var attempts int
	if err = tx.QueryRowContext(ctx, `SELECT attempts FROM document_ingest_jobs WHERE id=UUID_TO_BIN(?)`, selectedID).Scan(&attempts); err != nil {
		return document.IngestJob{}, err
	}
	if err = tx.Commit(); err != nil {
		return document.IngestJob{}, err
	}
	item.Status = "processing"
	return document.IngestJob{ID: selectedID, Document: item, Attempts: attempts}, nil
}

func (s *Store) SaveParsedDocument(ctx context.Context, job document.IngestJob, result document.ParseResult, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM document_chunks WHERE document_id=UUID_TO_BIN(?)`, job.Document.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM document_pages WHERE document_id=UUID_TO_BIN(?)`, job.Document.ID); err != nil {
		return err
	}
	for _, page := range result.Pages {
		if _, err = tx.ExecContext(ctx, `INSERT INTO document_pages (id,document_id,user_id,page_no,text_content,quality_score,parser_version,content_hash,created_at) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),UUID_TO_BIN(?),?,?,?,?,?,?)`, page.ID, job.Document.ID, job.Document.UserID, page.PageNo, page.Text, page.Quality, result.ParserVersion, page.ContentHash, now); err != nil {
			return err
		}
	}
	for _, chunk := range result.Chunks {
		if _, err = tx.ExecContext(ctx, `INSERT INTO document_chunks (id,point_id,document_id,user_id,ordinal_no,page_start,page_end,section_path,content,token_count,content_hash,parser_version,embedding_version,created_at) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),UUID_TO_BIN(?),UUID_TO_BIN(?),?,?,?,?,?,?,?,?,?,?)`, chunk.ID, chunk.PointID, job.Document.ID, job.Document.UserID, chunk.Ordinal, chunk.PageStart, chunk.PageEnd, chunk.SectionPath, chunk.Content, chunk.TokenCount, chunk.ContentHash, result.ParserVersion, chunk.EmbeddingVersion, now); err != nil {
			return err
		}
	}
	resultExec, err := tx.ExecContext(ctx, `UPDATE documents SET parser_version=?,page_count=?,chunk_count=?,updated_at=? WHERE id=UUID_TO_BIN(?) AND ingest_status='processing'`, result.ParserVersion, len(result.Pages), len(result.Chunks), now, job.Document.ID)
	if err != nil {
		return err
	}
	affected, _ := resultExec.RowsAffected()
	if affected == 0 {
		return document.ErrNotFound
	}
	return tx.Commit()
}

func (s *Store) CompleteIngestJob(ctx context.Context, jobID string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var documentID string
	if err = tx.QueryRowContext(ctx, `SELECT BIN_TO_UUID(document_id) FROM document_ingest_jobs WHERE id=UUID_TO_BIN(?) AND status='processing' FOR UPDATE`, jobID).Scan(&documentID); errors.Is(err, sql.ErrNoRows) {
		return document.ErrNotFound
	} else if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE document_ingest_jobs SET status='completed',finished_at=?,lease_expires_at=NULL,updated_at=? WHERE id=UUID_TO_BIN(?)`, now, now, jobID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE documents SET ingest_status='ready',failure_code=NULL,updated_at=? WHERE id=UUID_TO_BIN(?) AND ingest_status='processing'`, now, documentID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FailIngestJob(ctx context.Context, jobID, code string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var documentID string
	if err = tx.QueryRowContext(ctx, `SELECT BIN_TO_UUID(document_id) FROM document_ingest_jobs WHERE id=UUID_TO_BIN(?) FOR UPDATE`, jobID).Scan(&documentID); errors.Is(err, sql.ErrNoRows) {
		return document.ErrNotFound
	} else if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE document_ingest_jobs SET status='failed',last_error=?,finished_at=?,lease_expires_at=NULL,updated_at=? WHERE id=UUID_TO_BIN(?)`, code, now, now, jobID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE documents SET ingest_status='failed',failure_code=?,updated_at=? WHERE id=UUID_TO_BIN(?) AND ingest_status<>'deleted'`, code, now, documentID); err != nil {
		return err
	}
	return tx.Commit()
}

const documentSelect = `SELECT BIN_TO_UUID(d.id),BIN_TO_UUID(d.user_id),BIN_TO_UUID(d.file_id),BIN_TO_UUID(j.id),f.original_name,f.media_type,f.size_bytes,f.sha256,f.storage_key,d.ingest_status,COALESCE(d.parser_version,''),d.page_count,d.chunk_count,COALESCE(d.failure_code,''),d.created_at,d.updated_at,d.deleted_at FROM documents d JOIN files f ON f.id=d.file_id JOIN document_ingest_jobs j ON j.document_id=d.id`

func scanDocument(row rowScanner) (document.Document, error) {
	var item document.Document
	var deletedAt sql.NullTime
	err := row.Scan(&item.ID, &item.UserID, &item.FileID, &item.JobID, &item.Name, &item.MediaType, &item.SizeBytes, &item.SHA256, &item.StorageKey, &item.Status, &item.ParserVersion, &item.PageCount, &item.ChunkCount, &item.FailureCode, &item.CreatedAt, &item.UpdatedAt, &deletedAt)
	if deletedAt.Valid {
		item.DeletedAt = &deletedAt.Time
	}
	return item, err
}
