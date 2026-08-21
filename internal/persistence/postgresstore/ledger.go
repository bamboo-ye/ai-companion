package postgresstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/windcry1/ai-companion/internal/ledger"
	"github.com/windcry1/ai-companion/internal/platform/id"
)

var _ ledger.Store = (*Store)(nil)

func (s *Store) CreateCandidate(ctx context.Context, item ledger.Candidate) error {
	clarifications, err := json.Marshal(item.NeedsClarification)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO app.ledger_candidates (
			id,user_id,source_message_id,raw_text,direction,currency,amount_minor,
			category,merchant,occurred_at,timezone,time_precision,confidence,
			needs_clarification,status,created_at,updated_at
		) VALUES (
			$1,$2,NULLIF($3,'')::uuid,$4,NULLIF($5,''),NULLIF($6,''),
			NULLIF($7,0),$8,$9,$10,$11,NULLIF($12,''),$13,$14,$15,$16,$17
		)`,
		item.ID, item.UserID, item.SourceMessageID, item.RawText, item.Direction,
		item.Currency, item.AmountMinor, item.Category, item.Merchant, item.OccurredAt,
		item.Timezone, item.TimePrecision, item.Confidence, clarifications, item.Status,
		item.CreatedAt, item.UpdatedAt,
	)
	return err
}

func (s *Store) GetCandidate(ctx context.Context, userID, candidateID string) (ledger.Candidate, error) {
	item, err := scanLedgerCandidate(s.db.QueryRowContext(
		ctx, ledgerCandidateSelect+` WHERE id=$1 AND user_id=$2`, candidateID, userID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		err = ledger.ErrNotFound
	}
	return item, err
}

func (s *Store) ConfirmCandidate(ctx context.Context, candidate ledger.Candidate, entry ledger.Entry, idempotencyKey string, now time.Time) (ledger.Entry, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ledger.Entry{}, false, err
	}
	defer tx.Rollback()
	locked, err := scanLedgerCandidate(tx.QueryRowContext(
		ctx, ledgerCandidateSelect+` WHERE id=$1 AND user_id=$2 FOR UPDATE`,
		candidate.ID, candidate.UserID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return ledger.Entry{}, false, ledger.ErrNotFound
	}
	if err != nil {
		return ledger.Entry{}, false, err
	}
	existing, err := scanLedgerEntry(tx.QueryRowContext(
		ctx, ledgerEntrySelect+` WHERE user_id=$1 AND idempotency_key=$2 LIMIT 1`,
		candidate.UserID, idempotencyKey,
	))
	if err == nil {
		if existing.CandidateID != candidate.ID {
			return ledger.Entry{}, false, ledger.ErrIdempotencyReuse
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ledger.Entry{}, false, err
	}
	existing, err = scanLedgerEntry(tx.QueryRowContext(
		ctx, ledgerEntrySelect+` WHERE candidate_id=$1 LIMIT 1`, candidate.ID,
	))
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ledger.Entry{}, false, err
	}
	if locked.Status != "pending" || locked.OccurredAt == nil || len(locked.NeedsClarification) > 0 {
		return ledger.Entry{}, false, ledger.ErrConfirmation
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO app.ledger_entries (
			id,user_id,candidate_id,idempotency_key,direction,currency,amount_minor,
			category,merchant,occurred_at,timezone,note,status,created_at,updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'active',$13,$14)`,
		entry.ID, entry.UserID, entry.CandidateID, idempotencyKey, entry.Direction,
		entry.Currency, entry.AmountMinor, entry.Category, entry.Merchant,
		entry.OccurredAt, entry.Timezone, entry.Note, entry.CreatedAt, entry.UpdatedAt,
	); err != nil {
		return ledger.Entry{}, false, err
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE app.ledger_candidates SET status='confirmed',updated_at=$1 WHERE id=$2`,
		now, candidate.ID,
	); err != nil {
		return ledger.Entry{}, false, err
	}
	metadata, _ := json.Marshal(map[string]any{
		"candidate_id": candidate.ID,
		"amount_minor": entry.AmountMinor,
		"currency":     entry.Currency,
	})
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO eventing.audit_logs (
			actor_type,actor_id,action,resource_type,resource_id,metadata,occurred_at
		) VALUES ('user',$1,'ledger.entry.confirm','ledger_entry',$2,$3,$4)`,
		candidate.UserID, entry.ID, metadata, now,
	); err != nil {
		return ledger.Entry{}, false, err
	}
	eventID, err := id.New()
	if err != nil {
		return ledger.Entry{}, false, err
	}
	payload, _ := json.Marshal(map[string]string{
		"entry_id": entry.ID,
		"user_id":  candidate.UserID,
	})
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO eventing.outbox_events (
			id,aggregate_type,aggregate_id,event_type,event_version,payload,occurred_at
		) VALUES ($1,'ledger_entry',$2,'ledger.entry.created.v1',1,$3,$4)`,
		eventID, entry.ID, payload, now,
	); err != nil {
		return ledger.Entry{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return ledger.Entry{}, false, err
	}
	entry.IdempotencyKey = idempotencyKey
	return entry, true, nil
}

func (s *Store) ListEntries(ctx context.Context, userID string, filter ledger.EntryFilter) ([]ledger.Entry, error) {
	query := ledgerEntrySelect + ` WHERE user_id=$1 AND status='active'`
	args := []any{userID}
	if filter.Start != nil {
		args = append(args, *filter.Start)
		query += ` AND occurred_at>=$` + placeholder(len(args))
	}
	if filter.End != nil {
		args = append(args, *filter.End)
		query += ` AND occurred_at<$` + placeholder(len(args))
	}
	if filter.Direction != "" {
		args = append(args, filter.Direction)
		query += ` AND direction=$` + placeholder(len(args))
	}
	if filter.Category != "" {
		args = append(args, filter.Category)
		query += ` AND category=$` + placeholder(len(args))
	}
	args = append(args, filter.Limit)
	query += ` ORDER BY occurred_at DESC,created_at DESC LIMIT $` + placeholder(len(args))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ledger.Entry, 0)
	for rows.Next() {
		item, scanErr := scanLedgerEntry(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetEntry(ctx context.Context, userID, entryID string) (ledger.Entry, error) {
	item, err := scanLedgerEntry(s.db.QueryRowContext(
		ctx, ledgerEntrySelect+` WHERE id=$1 AND user_id=$2 AND status='active'`,
		entryID, userID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		err = ledger.ErrNotFound
	}
	return item, err
}

func (s *Store) UpdateEntry(ctx context.Context, item ledger.Entry) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE app.ledger_entries SET
			direction=$1,currency=$2,amount_minor=$3,category=$4,merchant=$5,
			occurred_at=$6,timezone=$7,note=$8,updated_at=$9
		WHERE id=$10 AND user_id=$11 AND status='active'`,
		item.Direction, item.Currency, item.AmountMinor, item.Category, item.Merchant,
		item.OccurredAt, item.Timezone, item.Note, item.UpdatedAt, item.ID, item.UserID,
	)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ledger.ErrNotFound
	}
	return nil
}

func (s *Store) DeleteEntry(ctx context.Context, userID, entryID string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE app.ledger_entries
		SET status='deleted',deleted_at=$1,updated_at=$1
		WHERE id=$2 AND user_id=$3 AND status='active'`,
		now, entryID, userID,
	)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ledger.ErrNotFound
	}
	return nil
}

func (s *Store) CreateExport(ctx context.Context, job ledger.ExportJob, requestKey string) (ledger.ExportJob, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ledger.ExportJob{}, false, err
	}
	defer tx.Rollback()
	existing, err := scanLedgerExport(tx.QueryRowContext(
		ctx, ledgerExportSelect+` WHERE e.user_id=$1 AND e.request_key=$2`,
		job.UserID, requestKey,
	))
	if err == nil {
		if existing.Month != job.Month || existing.Currency != job.Currency || existing.Timezone != job.Timezone {
			return ledger.ExportJob{}, false, ledger.ErrIdempotencyReuse
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ledger.ExportJob{}, false, err
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO app.ledger_exports (
			id,user_id,month,currency,timezone,request_key,status,available_at,created_at,updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,'queued',$7,$7,$8)`,
		job.ID, job.UserID, job.Month, job.Currency, job.Timezone, requestKey,
		job.CreatedAt, job.UpdatedAt,
	); err != nil {
		return ledger.ExportJob{}, false, err
	}
	eventID, err := id.New()
	if err != nil {
		return ledger.ExportJob{}, false, err
	}
	payload, _ := json.Marshal(map[string]string{"export_id": job.ID, "user_id": job.UserID})
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO eventing.outbox_events (
			id,aggregate_type,aggregate_id,event_type,event_version,payload,occurred_at
		) VALUES ($1,'ledger_export',$2,'ledger.export.v1',1,$3,$4)`,
		eventID, job.ID, payload, job.CreatedAt,
	); err != nil {
		return ledger.ExportJob{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return ledger.ExportJob{}, false, err
	}
	return job, true, nil
}

func (s *Store) GetExport(ctx context.Context, userID, exportID string) (ledger.ExportJob, error) {
	job, err := scanLedgerExport(s.db.QueryRowContext(
		ctx, ledgerExportSelect+` WHERE e.id=$1 AND e.user_id=$2`, exportID, userID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		err = ledger.ErrNotFound
	}
	return job, err
}

func (s *Store) ClaimExportByID(ctx context.Context, exportID, workerID string, now time.Time, lease time.Duration) (ledger.ExportJob, error) {
	return s.claimExport(ctx, exportID, workerID, now, lease)
}

func (s *Store) ClaimExport(ctx context.Context, workerID string, now time.Time, lease time.Duration) (ledger.ExportJob, error) {
	return s.claimExport(ctx, "", workerID, now, lease)
}

func (s *Store) claimExport(ctx context.Context, exportID, workerID string, now time.Time, lease time.Duration) (ledger.ExportJob, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ledger.ExportJob{}, err
	}
	defer tx.Rollback()
	job, err := scanLedgerExport(tx.QueryRowContext(ctx, ledgerExportSelect+`
		WHERE ($1='' OR e.id=$1::uuid)
			AND e.available_at<=$2
			AND (
				e.status='queued'
				OR (e.status='processing' AND e.lease_expires_at<=$2)
			)
		ORDER BY e.available_at,e.created_at
		LIMIT 1
		FOR UPDATE SKIP LOCKED`,
		exportID, now,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return ledger.ExportJob{}, ledger.ErrNotFound
	}
	if err != nil {
		return ledger.ExportJob{}, err
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE app.ledger_exports SET
			status='processing',attempts=attempts+1,worker_id=$1,
			lease_expires_at=$2,updated_at=$3
		WHERE id=$4`,
		workerID, now.Add(lease), now, job.ID,
	); err != nil {
		return ledger.ExportJob{}, err
	}
	if err = tx.Commit(); err != nil {
		return ledger.ExportJob{}, err
	}
	job.Status, job.WorkerID, job.UpdatedAt = "processing", workerID, now
	return job, nil
}

func (s *Store) CompleteExport(ctx context.Context, job ledger.ExportJob) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE app.ledger_exports SET
			status='completed',storage_key=$1,file_name=$2,media_type=$3,sha256=$4,
			size_bytes=$5,failure_code=NULL,last_error=NULL,worker_id=NULL,
			lease_expires_at=NULL,updated_at=$6,completed_at=$7
		WHERE id=$8 AND status='processing' AND worker_id=$9`,
		job.StorageKey, job.FileName, job.MediaType, job.SHA256, job.SizeBytes,
		job.UpdatedAt, job.CompletedAt, job.ID, job.WorkerID,
	)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ledger.ErrNotFound
	}
	return nil
}

func (s *Store) FailExport(ctx context.Context, exportID, workerID, code string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE app.ledger_exports SET
			status='failed',failure_code=$1,last_error=$1,worker_id=NULL,
			lease_expires_at=NULL,updated_at=$2,completed_at=$2
		WHERE id=$3 AND status='processing' AND worker_id=$4`,
		code, now, exportID, workerID,
	)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ledger.ErrNotFound
	}
	return nil
}

func (s *Store) ShareExportWithWorkspace(ctx context.Context, userID, workspaceID, exportID string, now time.Time) error {
	var exists bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM app.ledger_exports
			WHERE id=$1 AND user_id=$2 AND status='completed' AND storage_key IS NOT NULL
		)`,
		exportID, userID,
	).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return ledger.ErrNotFound
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO app.workspace_ledger_export_shares (
			workspace_id,export_id,shared_by,created_at
		) VALUES ($1,$2,$3,$4)
		ON CONFLICT (workspace_id,export_id) DO NOTHING`,
		workspaceID, exportID, userID, now,
	)
	return err
}

func (s *Store) ListWorkspaceExports(ctx context.Context, workspaceID string, limit int) ([]ledger.ExportJob, error) {
	rows, err := s.db.QueryContext(ctx, ledgerExportSelect+`
		JOIN app.workspace_ledger_export_shares wles ON wles.export_id=e.id
		WHERE wles.workspace_id=$1
			AND e.status='completed'
			AND e.storage_key IS NOT NULL
		ORDER BY wles.created_at DESC,e.created_at DESC
		LIMIT $2`,
		workspaceID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ledger.ExportJob, 0)
	for rows.Next() {
		item, scanErr := scanLedgerExport(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetWorkspaceExport(ctx context.Context, workspaceID, exportID string) (ledger.ExportJob, error) {
	item, err := scanLedgerExport(s.db.QueryRowContext(ctx, ledgerExportSelect+`
		JOIN app.workspace_ledger_export_shares wles ON wles.export_id=e.id
		WHERE wles.workspace_id=$1
			AND e.id=$2
			AND e.status='completed'
			AND e.storage_key IS NOT NULL`,
		workspaceID, exportID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return ledger.ExportJob{}, ledger.ErrNotFound
	}
	return item, err
}

const ledgerCandidateSelect = `
	SELECT
		id::text,user_id::text,COALESCE(source_message_id::text,''),raw_text,
		COALESCE(direction,''),COALESCE(currency,''),COALESCE(amount_minor,0),
		category,merchant,occurred_at,timezone,COALESCE(time_precision,''),
		confidence,needs_clarification,status,created_at,updated_at
	FROM app.ledger_candidates`

const ledgerEntrySelect = `
	SELECT
		id::text,user_id::text,COALESCE(candidate_id::text,''),idempotency_key,
		direction,currency,amount_minor,category,merchant,occurred_at,timezone,
		note,status,created_at,updated_at,deleted_at
	FROM app.ledger_entries`

const ledgerExportSelect = `
	SELECT
		e.id::text,e.user_id::text,e.month,e.currency,e.timezone,e.status,
		COALESCE(e.storage_key,''),COALESCE(e.file_name,''),
		COALESCE(e.media_type,''),COALESCE(e.size_bytes,0),
		COALESCE(e.sha256,''),COALESCE(e.failure_code,''),
		e.created_at,e.updated_at,e.completed_at,COALESCE(e.worker_id,'')
	FROM app.ledger_exports e`

func scanLedgerCandidate(row rowScanner) (ledger.Candidate, error) {
	var item ledger.Candidate
	var occurred sql.NullTime
	var clarifications []byte
	err := row.Scan(
		&item.ID, &item.UserID, &item.SourceMessageID, &item.RawText,
		&item.Direction, &item.Currency, &item.AmountMinor, &item.Category,
		&item.Merchant, &occurred, &item.Timezone, &item.TimePrecision,
		&item.Confidence, &clarifications, &item.Status, &item.CreatedAt,
		&item.UpdatedAt,
	)
	if err != nil {
		return item, err
	}
	if occurred.Valid {
		item.OccurredAt = &occurred.Time
	}
	if len(clarifications) > 0 {
		err = json.Unmarshal(clarifications, &item.NeedsClarification)
	}
	return item, err
}

func scanLedgerEntry(row rowScanner) (ledger.Entry, error) {
	var item ledger.Entry
	var deleted sql.NullTime
	err := row.Scan(
		&item.ID, &item.UserID, &item.CandidateID, &item.IdempotencyKey,
		&item.Direction, &item.Currency, &item.AmountMinor, &item.Category,
		&item.Merchant, &item.OccurredAt, &item.Timezone, &item.Note, &item.Status,
		&item.CreatedAt, &item.UpdatedAt, &deleted,
	)
	if deleted.Valid {
		item.DeletedAt = &deleted.Time
	}
	return item, err
}

func scanLedgerExport(row rowScanner) (ledger.ExportJob, error) {
	var job ledger.ExportJob
	var completed sql.NullTime
	err := row.Scan(
		&job.ID, &job.UserID, &job.Month, &job.Currency, &job.Timezone,
		&job.Status, &job.StorageKey, &job.FileName, &job.MediaType,
		&job.SizeBytes, &job.SHA256, &job.FailureCode, &job.CreatedAt,
		&job.UpdatedAt, &completed, &job.WorkerID,
	)
	if completed.Valid {
		job.CompletedAt = &completed.Time
	}
	return job, err
}
