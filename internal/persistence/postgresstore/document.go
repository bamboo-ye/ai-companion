package postgresstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/document"
	"github.com/windcry1/ai-companion/internal/platform/id"
)

var (
	_ document.Store         = (*Store)(nil)
	_ document.ParsedStore   = (*Store)(nil)
	_ document.SourceIRStore = (*Store)(nil)
	_ document.IngestStore   = (*Store)(nil)
	_ document.CleanupStore  = (*Store)(nil)
)

func (s *Store) CreateDocument(ctx context.Context, item document.Document) (document.Document, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return item, false, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO app.files (
			id,user_id,original_name,media_type,size_bytes,sha256,storage_key,
			status,created_at,updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,'active',$8,$9)
		ON CONFLICT (user_id,sha256) WHERE status='active' DO NOTHING`,
		item.FileID, item.UserID, item.Name, item.MediaType, item.SizeBytes,
		item.SHA256, item.StorageKey, item.CreatedAt, item.UpdatedAt,
	); err != nil {
		return item, false, err
	}
	var storedFileID string
	if err = tx.QueryRowContext(ctx, `
		SELECT id::text
		FROM app.files
		WHERE user_id=$1 AND sha256=$2 AND status='active'`,
		item.UserID, item.SHA256,
	).Scan(&storedFileID); err != nil {
		return item, false, err
	}
	if storedFileID != item.FileID {
		existing, scanErr := scanDocument(tx.QueryRowContext(ctx, documentSelect+`
			WHERE f.id=$1 AND d.ingest_status<>'deleted'`,
			storedFileID,
		))
		return existing, false, scanErr
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO app.documents (
			id,user_id,file_id,ingest_status,page_count,chunk_count,created_at,updated_at
		) VALUES ($1,$2,$3,$4,0,0,$5,$6)`,
		item.ID, item.UserID, item.FileID, item.Status, item.CreatedAt, item.UpdatedAt,
	); err != nil {
		return item, false, err
	}
	idempotencyKey := "document.ingest.v1:" + item.ID
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO app.document_ingest_jobs (
			id,document_id,user_id,status,idempotency_key,available_at,created_at,updated_at
		) VALUES ($1,$2,$3,'queued',$4,$5,$5,$6)`,
		item.JobID, item.ID, item.UserID, idempotencyKey,
		item.CreatedAt, item.UpdatedAt,
	); err != nil {
		return item, false, err
	}
	eventID, err := id.New()
	if err != nil {
		return item, false, err
	}
	payload, err := json.Marshal(map[string]string{
		"document_id": item.ID,
		"job_id":      item.JobID,
		"user_id":     item.UserID,
	})
	if err != nil {
		return item, false, err
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO eventing.outbox_events (
			id,aggregate_type,aggregate_id,event_type,event_version,payload,occurred_at
		) VALUES ($1,'document',$2,'document.ingest.v1',1,$3,$4)`,
		eventID, item.ID, payload, item.CreatedAt,
	); err != nil {
		return item, false, err
	}
	if err = tx.Commit(); err != nil {
		return item, false, err
	}
	return item, true, nil
}

func (s *Store) ListDocuments(ctx context.Context, userID string, limit int) ([]document.Document, error) {
	rows, err := s.db.QueryContext(ctx, documentSelect+`
		WHERE d.user_id=$1
			AND d.ingest_status<>'deleted'
			AND f.status='active'
		ORDER BY d.created_at DESC
		LIMIT $2`,
		userID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]document.Document, 0)
	for rows.Next() {
		item, scanErr := scanDocument(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetDocument(ctx context.Context, userID, documentID string) (document.Document, error) {
	item, err := scanDocument(s.db.QueryRowContext(ctx, documentSelect+`
		WHERE d.id=$1
			AND d.user_id=$2
			AND d.ingest_status<>'deleted'
			AND f.status='active'`,
		documentID, userID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		err = document.ErrNotFound
	}
	return item, err
}

func (s *Store) ListDocumentChunks(
	ctx context.Context,
	userID string,
	documentID string,
	limit int,
) ([]document.Chunk, error) {
	if limit <= 0 {
		limit = 10_000
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id::text,point_id::text,ordinal_no,page_start,page_end,section_path,
			content,token_count,content_hash,parser_version,embedding_version
		FROM app.document_chunks
		WHERE user_id=$1 AND document_id=$2
		ORDER BY ordinal_no
		LIMIT $3`,
		userID, documentID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	chunks := make([]document.Chunk, 0)
	for rows.Next() {
		var chunk document.Chunk
		if err = rows.Scan(
			&chunk.ID, &chunk.PointID, &chunk.Ordinal, &chunk.PageStart, &chunk.PageEnd,
			&chunk.SectionPath, &chunk.Content, &chunk.TokenCount, &chunk.ContentHash,
			&chunk.ParserVersion, &chunk.EmbeddingVersion,
		); err != nil {
			return nil, err
		}
		chunks = append(chunks, chunk)
	}
	return chunks, rows.Err()
}

func (s *Store) GetDocumentSourceIR(
	ctx context.Context,
	userID string,
	documentID string,
) (map[string]any, error) {
	var encoded []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT d.source_ir
		FROM app.documents d
		JOIN app.files f ON f.id=d.file_id
		WHERE d.id=$1 AND d.user_id=$2 AND d.ingest_status='ready'
			AND f.status='active' AND d.source_ir IS NOT NULL`,
		documentID, userID,
	).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, document.ErrParsedContentUnavailable
	}
	if err != nil {
		return nil, err
	}
	var sourceIR map[string]any
	if err = json.Unmarshal(encoded, &sourceIR); err != nil {
		return nil, err
	}
	return sourceIR, nil
}

func (s *Store) DeleteDocument(ctx context.Context, userID, documentID string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var fileID, storageKey string
	err = tx.QueryRowContext(ctx, `
		SELECT d.file_id::text,f.storage_key
		FROM app.documents d
		JOIN app.files f ON f.id=d.file_id
		WHERE d.id=$1 AND d.user_id=$2 AND d.ingest_status<>'deleted'
		FOR UPDATE OF d,f`,
		documentID, userID,
	).Scan(&fileID, &storageKey)
	if errors.Is(err, sql.ErrNoRows) {
		return document.ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE app.documents
		SET ingest_status='deleted',deleted_at=$1,updated_at=$1
		WHERE id=$2`,
		now, documentID,
	); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE app.files
		SET status='deleted',deleted_at=$1,updated_at=$1
		WHERE id=$2`,
		now, fileID,
	); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE app.document_ingest_jobs
		SET status='cancelled',finished_at=$1,worker_id=NULL,
			lease_expires_at=NULL,updated_at=$1
		WHERE document_id=$2 AND status IN ('queued','processing')`,
		now, documentID,
	); err != nil {
		return err
	}
	cleanupID, err := id.New()
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO app.document_cleanup_jobs (
			id,document_id,user_id,storage_key,status,available_at,created_at,updated_at
		) VALUES ($1,$2,$3,$4,'queued',$5,$5,$5)`,
		cleanupID, documentID, userID, storageKey, now,
	); err != nil {
		return err
	}
	eventID, err := id.New()
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{
		"job_id":      cleanupID,
		"document_id": documentID,
		"user_id":     userID,
	})
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO eventing.outbox_events (
			id,aggregate_type,aggregate_id,event_type,event_version,payload,occurred_at
		) VALUES ($1,'document',$2,'document.cleanup.v1',1,$3,$4)`,
		eventID, documentID, payload, now,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ShareDocumentWithWorkspace(ctx context.Context, userID, workspaceID, documentID string, now time.Time) error {
	if _, err := s.GetDocument(ctx, userID, documentID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO app.workspace_document_shares (
			workspace_id,document_id,shared_by,created_at
		) VALUES ($1,$2,$3,$4)
		ON CONFLICT (workspace_id,document_id) DO NOTHING`,
		workspaceID, documentID, userID, now,
	)
	return err
}

func (s *Store) ListWorkspaceDocuments(ctx context.Context, workspaceID string, limit int) ([]document.Document, error) {
	rows, err := s.db.QueryContext(ctx, documentSelect+`
		JOIN app.workspace_document_shares wds ON wds.document_id=d.id
		WHERE wds.workspace_id=$1
			AND d.ingest_status<>'deleted'
			AND f.status='active'
		ORDER BY wds.created_at DESC,d.created_at DESC
		LIMIT $2`,
		workspaceID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]document.Document, 0)
	for rows.Next() {
		item, scanErr := scanDocument(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) DurableCleanupDispatch() bool { return true }

func (s *Store) ClaimCleanupJobByID(ctx context.Context, jobID, workerID string, now time.Time, lease time.Duration) (document.CleanupJob, error) {
	return s.claimCleanupJob(ctx, jobID, workerID, now, lease)
}

func (s *Store) ClaimCleanupJob(ctx context.Context, workerID string, now time.Time, lease time.Duration) (document.CleanupJob, error) {
	return s.claimCleanupJob(ctx, "", workerID, now, lease)
}

func (s *Store) claimCleanupJob(ctx context.Context, jobID, workerID string, now time.Time, lease time.Duration) (document.CleanupJob, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return document.CleanupJob{}, err
	}
	defer tx.Rollback()
	var job document.CleanupJob
	err = tx.QueryRowContext(ctx, `
		SELECT id::text,document_id::text,user_id::text,storage_key,attempts
		FROM app.document_cleanup_jobs
		WHERE ($1='' OR id=$1::uuid)
			AND available_at<=$2
			AND (
				status='queued'
				OR (status='processing' AND lease_expires_at<=$2)
			)
		ORDER BY available_at,created_at
		LIMIT 1
		FOR UPDATE SKIP LOCKED`,
		jobID, now,
	).Scan(&job.ID, &job.DocumentID, &job.UserID, &job.StorageKey, &job.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return document.CleanupJob{}, document.ErrNoCleanupJob
	}
	if err != nil {
		return document.CleanupJob{}, err
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE app.document_cleanup_jobs SET
			status='processing',attempts=attempts+1,worker_id=$1,
			lease_expires_at=$2,last_error=NULL,updated_at=$3
		WHERE id=$4`,
		workerID, now.Add(lease), now, job.ID,
	); err != nil {
		return document.CleanupJob{}, err
	}
	if err = tx.Commit(); err != nil {
		return document.CleanupJob{}, err
	}
	job.Attempts++
	return job, nil
}

func (s *Store) CompleteCleanupJob(ctx context.Context, job document.CleanupJob, workerID string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE app.document_cleanup_jobs SET
			status='completed',worker_id=NULL,lease_expires_at=NULL,last_error=NULL,
			updated_at=$1,completed_at=$1
		WHERE id=$2 AND status='processing' AND worker_id=$3`,
		now, job.ID, workerID,
	)
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
	result, err := tx.ExecContext(ctx, `
		UPDATE app.document_cleanup_jobs SET
			status='queued',available_at=$1,worker_id=NULL,lease_expires_at=NULL,
			last_error=$2,updated_at=$3
		WHERE id=$4 AND status='processing' AND worker_id=$5`,
		retryAt, message, now, job.ID, workerID,
	)
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
	payload, _ := json.Marshal(map[string]string{
		"job_id":      job.ID,
		"document_id": job.DocumentID,
		"user_id":     job.UserID,
	})
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO eventing.outbox_events (
			id,aggregate_type,aggregate_id,event_type,event_version,payload,
			occurred_at,status,available_at
		) VALUES ($1,'document',$2,'document.cleanup.v1',1,$3,$4,'pending',$5)`,
		eventID, job.DocumentID, payload, now, retryAt,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ClaimIngestJob(ctx context.Context, workerID string, now time.Time, lease time.Duration) (document.IngestJob, error) {
	return s.claimIngestJob(ctx, "", workerID, now, lease)
}

func (s *Store) ClaimIngestJobByID(ctx context.Context, jobID, workerID string, now time.Time, lease time.Duration) (document.IngestJob, error) {
	return s.claimIngestJob(ctx, jobID, workerID, now, lease)
}

func (s *Store) claimIngestJob(ctx context.Context, jobID, workerID string, now time.Time, lease time.Duration) (document.IngestJob, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return document.IngestJob{}, err
	}
	defer tx.Rollback()
	var selectedID string
	err = tx.QueryRowContext(ctx, `
		SELECT j.id::text
		FROM app.document_ingest_jobs j
		JOIN app.documents d ON d.id=j.document_id
		WHERE ($1='' OR j.id=$1::uuid)
			AND d.ingest_status<>'deleted'
			AND j.available_at<=$2
			AND (
				j.status='queued'
				OR (j.status='processing' AND j.lease_expires_at<=$2)
			)
		ORDER BY j.available_at,j.created_at
		LIMIT 1
		FOR UPDATE OF j SKIP LOCKED`,
		jobID, now,
	).Scan(&selectedID)
	if errors.Is(err, sql.ErrNoRows) {
		return document.IngestJob{}, document.ErrNoIngestJob
	}
	if err != nil {
		return document.IngestJob{}, err
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE app.document_ingest_jobs SET
			status='processing',attempts=attempts+1,worker_id=$1,
			lease_expires_at=$2,started_at=COALESCE(started_at,$3),updated_at=$3
		WHERE id=$4`,
		workerID, now.Add(lease), now, selectedID,
	); err != nil {
		return document.IngestJob{}, err
	}
	item, err := scanDocument(tx.QueryRowContext(
		ctx, documentSelect+` WHERE j.id=$1`, selectedID,
	))
	if err != nil {
		return document.IngestJob{}, err
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE app.documents
		SET ingest_status='processing',failure_code=NULL,updated_at=$1
		WHERE id=$2`,
		now, item.ID,
	); err != nil {
		return document.IngestJob{}, err
	}
	var attempts int
	if err = tx.QueryRowContext(ctx, `
		SELECT attempts FROM app.document_ingest_jobs WHERE id=$1`,
		selectedID,
	).Scan(&attempts); err != nil {
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
	if _, err = tx.ExecContext(ctx, `
		DELETE FROM app.document_chunks WHERE document_id=$1`,
		job.Document.ID,
	); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `
		DELETE FROM app.document_pages WHERE document_id=$1`,
		job.Document.ID,
	); err != nil {
		return err
	}
	for _, page := range result.Pages {
		if _, err = tx.ExecContext(ctx, `
			INSERT INTO app.document_pages (
				id,document_id,user_id,page_no,text_content,quality_score,
				parser_version,content_hash,created_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			page.ID, job.Document.ID, job.Document.UserID, page.PageNo, page.Text,
			page.Quality, result.ParserVersion, page.ContentHash, now,
		); err != nil {
			return err
		}
	}
	for _, chunk := range result.Chunks {
		if _, err = tx.ExecContext(ctx, `
			INSERT INTO app.document_chunks (
				id,point_id,document_id,user_id,ordinal_no,page_start,page_end,
				section_path,content,token_count,content_hash,parser_version,
				embedding_version,created_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
			chunk.ID, chunk.PointID, job.Document.ID, job.Document.UserID,
			chunk.Ordinal, chunk.PageStart, chunk.PageEnd, chunk.SectionPath,
			chunk.Content, chunk.TokenCount, chunk.ContentHash,
			result.ParserVersion, chunk.EmbeddingVersion, now,
		); err != nil {
			return err
		}
	}
	var sourceIRJSON any
	sourceIRVersion := ""
	if result.SourceIR != nil {
		encoded, marshalErr := json.Marshal(result.SourceIR)
		if marshalErr != nil {
			return marshalErr
		}
		sourceIRJSON = string(encoded)
		sourceIRVersion = strings.TrimSpace(stringValue(result.SourceIR["version"]))
	}
	resultExec, err := tx.ExecContext(ctx, `
		UPDATE app.documents SET
			parser_version=$1,page_count=$2,chunk_count=$3,source_ir_version=$4,
			source_ir=$5,updated_at=$6
		WHERE id=$7 AND ingest_status='processing'`,
		result.ParserVersion, len(result.Pages), len(result.Chunks), sourceIRVersion,
		sourceIRJSON, now, job.Document.ID,
	)
	if err != nil {
		return err
	}
	if affected, _ := resultExec.RowsAffected(); affected == 0 {
		return document.ErrNotFound
	}
	return tx.Commit()
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func (s *Store) CompleteIngestJob(ctx context.Context, jobID string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var documentID string
	err = tx.QueryRowContext(ctx, `
		SELECT document_id::text
		FROM app.document_ingest_jobs
		WHERE id=$1 AND status='processing'
		FOR UPDATE`,
		jobID,
	).Scan(&documentID)
	if errors.Is(err, sql.ErrNoRows) {
		return document.ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE app.document_ingest_jobs SET
			status='completed',finished_at=$1,worker_id=NULL,
			lease_expires_at=NULL,updated_at=$1
		WHERE id=$2`,
		now, jobID,
	); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE app.documents
		SET ingest_status='ready',failure_code=NULL,updated_at=$1
		WHERE id=$2 AND ingest_status='processing'`,
		now, documentID,
	); err != nil {
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
	err = tx.QueryRowContext(ctx, `
		SELECT document_id::text
		FROM app.document_ingest_jobs
		WHERE id=$1
		FOR UPDATE`,
		jobID,
	).Scan(&documentID)
	if errors.Is(err, sql.ErrNoRows) {
		return document.ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE app.document_ingest_jobs SET
			status='failed',last_error=$1,finished_at=$2,worker_id=NULL,
			lease_expires_at=NULL,updated_at=$2
		WHERE id=$3`,
		code, now, jobID,
	); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE app.documents
		SET ingest_status='failed',failure_code=$1,updated_at=$2
		WHERE id=$3 AND ingest_status<>'deleted'`,
		code, now, documentID,
	); err != nil {
		return err
	}
	return tx.Commit()
}

const documentSelect = `
	SELECT
		d.id::text,d.user_id::text,d.file_id::text,j.id::text,
		f.original_name,f.media_type,f.size_bytes,f.sha256,f.storage_key,
		d.ingest_status,COALESCE(d.parser_version,''),d.page_count,d.chunk_count,
		COALESCE(d.failure_code,''),d.created_at,d.updated_at,d.deleted_at
	FROM app.documents d
	JOIN app.files f ON f.id=d.file_id
	JOIN app.document_ingest_jobs j ON j.document_id=d.id`

func scanDocument(row rowScanner) (document.Document, error) {
	var item document.Document
	var deletedAt sql.NullTime
	err := row.Scan(
		&item.ID, &item.UserID, &item.FileID, &item.JobID, &item.Name,
		&item.MediaType, &item.SizeBytes, &item.SHA256, &item.StorageKey,
		&item.Status, &item.ParserVersion, &item.PageCount, &item.ChunkCount,
		&item.FailureCode, &item.CreatedAt, &item.UpdatedAt, &deletedAt,
	)
	if deletedAt.Valid {
		item.DeletedAt = &deletedAt.Time
	}
	return item, err
}
