package mysqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/windcry1/ai-companion/internal/ledger"
	"github.com/windcry1/ai-companion/internal/platform/id"
)

func (s *Store) CreateCandidate(ctx context.Context, item ledger.Candidate) error {
	clarifications, err := json.Marshal(item.NeedsClarification)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO ledger_candidates (id,user_id,source_message_id,raw_text,direction,currency,amount_minor,category,merchant,occurred_at,timezone,time_precision,confidence,needs_clarification,status,created_at,updated_at) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),UUID_TO_BIN(NULLIF(?,'')),?,NULLIF(?,''),NULLIF(?,''),NULLIF(?,0),?,?,?, ?,NULLIF(?,''),?,?,?, ?,?)`, item.ID, item.UserID, item.SourceMessageID, item.RawText, item.Direction, item.Currency, item.AmountMinor, item.Category, item.Merchant, item.OccurredAt, item.Timezone, item.TimePrecision, item.Confidence, clarifications, item.Status, item.CreatedAt, item.UpdatedAt)
	return err
}

func (s *Store) GetCandidate(ctx context.Context, userID, candidateID string) (ledger.Candidate, error) {
	item, err := scanLedgerCandidate(s.db.QueryRowContext(ctx, ledgerCandidateSelect+` WHERE id=UUID_TO_BIN(?) AND user_id=UUID_TO_BIN(?)`, candidateID, userID))
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
	locked, err := scanLedgerCandidate(tx.QueryRowContext(ctx, ledgerCandidateSelect+` WHERE id=UUID_TO_BIN(?) AND user_id=UUID_TO_BIN(?) FOR UPDATE`, candidate.ID, candidate.UserID))
	if errors.Is(err, sql.ErrNoRows) {
		return ledger.Entry{}, false, ledger.ErrNotFound
	}
	if err != nil {
		return ledger.Entry{}, false, err
	}
	existing, err := scanLedgerEntry(tx.QueryRowContext(ctx, ledgerEntrySelect+` WHERE user_id=UUID_TO_BIN(?) AND idempotency_key=? LIMIT 1`, candidate.UserID, idempotencyKey))
	if err == nil {
		if existing.CandidateID != candidate.ID {
			return ledger.Entry{}, false, ledger.ErrIdempotencyReuse
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ledger.Entry{}, false, err
	}
	existing, err = scanLedgerEntry(tx.QueryRowContext(ctx, ledgerEntrySelect+` WHERE candidate_id=UUID_TO_BIN(?) LIMIT 1`, candidate.ID))
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ledger.Entry{}, false, err
	}
	if locked.Status != "pending" || locked.OccurredAt == nil || len(locked.NeedsClarification) > 0 {
		return ledger.Entry{}, false, ledger.ErrConfirmation
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO ledger_entries (id,user_id,candidate_id,idempotency_key,direction,currency,amount_minor,category,merchant,occurred_at,timezone,note,status,created_at,updated_at) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),UUID_TO_BIN(?),?,?,?,?,?,?,?,?,?,'active',?,?)`, entry.ID, entry.UserID, entry.CandidateID, idempotencyKey, entry.Direction, entry.Currency, entry.AmountMinor, entry.Category, entry.Merchant, entry.OccurredAt, entry.Timezone, entry.Note, entry.CreatedAt, entry.UpdatedAt)
	if err != nil {
		return ledger.Entry{}, false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE ledger_candidates SET status='confirmed',updated_at=? WHERE id=UUID_TO_BIN(?)`, now, candidate.ID); err != nil {
		return ledger.Entry{}, false, err
	}
	metadata, _ := json.Marshal(map[string]any{"candidate_id": candidate.ID, "amount_minor": entry.AmountMinor, "currency": entry.Currency})
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_logs (actor_type,actor_id,action,resource_type,resource_id,metadata,occurred_at) VALUES ('user',UUID_TO_BIN(?),'ledger.entry.confirm','ledger_entry',UUID_TO_BIN(?),?,?)`, candidate.UserID, entry.ID, metadata, now); err != nil {
		return ledger.Entry{}, false, err
	}
	eventID, err := id.New()
	if err != nil {
		return ledger.Entry{}, false, err
	}
	payload, _ := json.Marshal(map[string]string{"entry_id": entry.ID, "user_id": candidate.UserID})
	if _, err = tx.ExecContext(ctx, `INSERT INTO outbox_events (id,aggregate_type,aggregate_id,event_type,event_version,payload,occurred_at) VALUES (UUID_TO_BIN(?),'ledger_entry',UUID_TO_BIN(?),'ledger.entry.created.v1',1,?,?)`, eventID, entry.ID, payload, now); err != nil {
		return ledger.Entry{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return ledger.Entry{}, false, err
	}
	return entry, true, nil
}

func (s *Store) ListEntries(ctx context.Context, userID string, filter ledger.EntryFilter) ([]ledger.Entry, error) {
	query := ledgerEntrySelect + ` WHERE user_id=UUID_TO_BIN(?) AND status='active'`
	args := []any{userID}
	if filter.Start != nil {
		query += ` AND occurred_at>=?`
		args = append(args, *filter.Start)
	}
	if filter.End != nil {
		query += ` AND occurred_at<?`
		args = append(args, *filter.End)
	}
	if filter.Direction != "" {
		query += ` AND direction=?`
		args = append(args, filter.Direction)
	}
	if filter.Category != "" {
		query += ` AND category=?`
		args = append(args, filter.Category)
	}
	query += ` ORDER BY occurred_at DESC,created_at DESC LIMIT ?`
	args = append(args, filter.Limit)
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
	item, err := scanLedgerEntry(s.db.QueryRowContext(ctx, ledgerEntrySelect+` WHERE id=UUID_TO_BIN(?) AND user_id=UUID_TO_BIN(?) AND status='active'`, entryID, userID))
	if errors.Is(err, sql.ErrNoRows) {
		err = ledger.ErrNotFound
	}
	return item, err
}

func (s *Store) UpdateEntry(ctx context.Context, item ledger.Entry) error {
	result, err := s.db.ExecContext(ctx, `UPDATE ledger_entries SET direction=?,currency=?,amount_minor=?,category=?,merchant=?,occurred_at=?,timezone=?,note=?,updated_at=? WHERE id=UUID_TO_BIN(?) AND user_id=UUID_TO_BIN(?) AND status='active'`, item.Direction, item.Currency, item.AmountMinor, item.Category, item.Merchant, item.OccurredAt, item.Timezone, item.Note, item.UpdatedAt, item.ID, item.UserID)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return ledger.ErrNotFound
	}
	return nil
}

func (s *Store) DeleteEntry(ctx context.Context, userID, entryID string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE ledger_entries SET status='deleted',deleted_at=?,updated_at=? WHERE id=UUID_TO_BIN(?) AND user_id=UUID_TO_BIN(?) AND status='active'`, now, now, entryID, userID)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
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
	existing, err := scanLedgerExport(tx.QueryRowContext(ctx, ledgerExportSelect+` WHERE user_id=UUID_TO_BIN(?) AND request_key=?`, job.UserID, requestKey))
	if err == nil {
		if existing.Month != job.Month || existing.Currency != job.Currency || existing.Timezone != job.Timezone {
			return ledger.ExportJob{}, false, ledger.ErrIdempotencyReuse
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ledger.ExportJob{}, false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO ledger_exports (id,user_id,month,currency,timezone,request_key,status,available_at,created_at,updated_at) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),?,?,?,?, 'queued',?,?,?)`, job.ID, job.UserID, job.Month, job.Currency, job.Timezone, requestKey, job.CreatedAt, job.CreatedAt, job.UpdatedAt)
	if err != nil {
		return ledger.ExportJob{}, false, err
	}
	eventID, err := id.New()
	if err != nil {
		return ledger.ExportJob{}, false, err
	}
	payload, _ := json.Marshal(map[string]string{"export_id": job.ID, "user_id": job.UserID})
	if _, err = tx.ExecContext(ctx, `INSERT INTO outbox_events (id,aggregate_type,aggregate_id,event_type,event_version,payload,occurred_at) VALUES (UUID_TO_BIN(?),'ledger_export',UUID_TO_BIN(?),'ledger.export.v1',1,?,?)`, eventID, job.ID, payload, job.CreatedAt); err != nil {
		return ledger.ExportJob{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return ledger.ExportJob{}, false, err
	}
	return job, true, nil
}

func (s *Store) GetExport(ctx context.Context, userID, exportID string) (ledger.ExportJob, error) {
	job, err := scanLedgerExport(s.db.QueryRowContext(ctx, ledgerExportSelect+` WHERE id=UUID_TO_BIN(?) AND user_id=UUID_TO_BIN(?)`, exportID, userID))
	if errors.Is(err, sql.ErrNoRows) {
		err = ledger.ErrNotFound
	}
	return job, err
}

func (s *Store) ClaimExportByID(ctx context.Context, exportID, workerID string, now time.Time, lease time.Duration) (ledger.ExportJob, error) {
	return s.claimExport(ctx, `id=UUID_TO_BIN(?)`, []any{exportID}, workerID, now, lease)
}

func (s *Store) ClaimExport(ctx context.Context, workerID string, now time.Time, lease time.Duration) (ledger.ExportJob, error) {
	return s.claimExport(ctx, `1=1`, nil, workerID, now, lease)
}

func (s *Store) claimExport(ctx context.Context, predicate string, args []any, workerID string, now time.Time, lease time.Duration) (ledger.ExportJob, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ledger.ExportJob{}, err
	}
	defer tx.Rollback()
	queryArgs := append(append([]any{}, args...), now, now)
	job, err := scanLedgerExport(tx.QueryRowContext(ctx, ledgerExportSelect+` WHERE `+predicate+` AND available_at<=? AND (status='queued' OR (status='processing' AND lease_expires_at<=?)) ORDER BY available_at,created_at LIMIT 1 FOR UPDATE SKIP LOCKED`, queryArgs...))
	if errors.Is(err, sql.ErrNoRows) {
		return ledger.ExportJob{}, ledger.ErrNotFound
	}
	if err != nil {
		return ledger.ExportJob{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE ledger_exports SET status='processing',attempts=attempts+1,worker_id=?,lease_expires_at=?,updated_at=? WHERE id=UUID_TO_BIN(?)`, workerID, now.Add(lease), now, job.ID); err != nil {
		return ledger.ExportJob{}, err
	}
	if err = tx.Commit(); err != nil {
		return ledger.ExportJob{}, err
	}
	job.Status, job.WorkerID, job.UpdatedAt = "processing", workerID, now
	return job, nil
}

func (s *Store) CompleteExport(ctx context.Context, job ledger.ExportJob) error {
	result, err := s.db.ExecContext(ctx, `UPDATE ledger_exports SET status='completed',storage_key=?,file_name=?,media_type=?,sha256=?,size_bytes=?,failure_code=NULL,last_error=NULL,worker_id=NULL,lease_expires_at=NULL,updated_at=?,completed_at=? WHERE id=UUID_TO_BIN(?) AND status='processing' AND worker_id=?`, job.StorageKey, job.FileName, job.MediaType, job.SHA256, job.SizeBytes, job.UpdatedAt, job.CompletedAt, job.ID, job.WorkerID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ledger.ErrNotFound
	}
	return nil
}

func (s *Store) FailExport(ctx context.Context, exportID, workerID, code string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE ledger_exports SET status='failed',failure_code=?,last_error=?,worker_id=NULL,lease_expires_at=NULL,updated_at=?,completed_at=? WHERE id=UUID_TO_BIN(?) AND status='processing' AND worker_id=?`, code, code, now, now, exportID, workerID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ledger.ErrNotFound
	}
	return nil
}

func (s *Store) ShareExportWithWorkspace(ctx context.Context, userID, workspaceID, exportID string, now time.Time) error {
	if _, err := s.GetExport(ctx, userID, exportID); err != nil {
		return err
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM ledger_exports WHERE id=UUID_TO_BIN(?) AND user_id=UUID_TO_BIN(?) AND status='completed' AND storage_key IS NOT NULL`, exportID, userID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return ledger.ErrNotFound
	} else if err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO workspace_ledger_export_shares (workspace_id,export_id,shared_by,created_at) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),UUID_TO_BIN(?),?) ON DUPLICATE KEY UPDATE created_at=created_at`, workspaceID, exportID, userID, now)
	return err
}

func (s *Store) ListWorkspaceExports(ctx context.Context, workspaceID string, limit int) ([]ledger.ExportJob, error) {
	rows, err := s.db.QueryContext(ctx, ledgerExportSelect+` JOIN workspace_ledger_export_shares wles ON wles.export_id=ledger_exports.id WHERE wles.workspace_id=UUID_TO_BIN(?) AND ledger_exports.status='completed' AND ledger_exports.storage_key IS NOT NULL ORDER BY wles.created_at DESC,ledger_exports.created_at DESC LIMIT ?`, workspaceID, limit)
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
	item, err := scanLedgerExport(s.db.QueryRowContext(ctx, ledgerExportSelect+` JOIN workspace_ledger_export_shares wles ON wles.export_id=ledger_exports.id WHERE wles.workspace_id=UUID_TO_BIN(?) AND ledger_exports.id=UUID_TO_BIN(?) AND ledger_exports.status='completed' AND ledger_exports.storage_key IS NOT NULL`, workspaceID, exportID))
	if errors.Is(err, sql.ErrNoRows) {
		return ledger.ExportJob{}, ledger.ErrNotFound
	}
	return item, err
}

const ledgerCandidateSelect = `SELECT BIN_TO_UUID(id),BIN_TO_UUID(user_id),COALESCE(BIN_TO_UUID(source_message_id),''),raw_text,COALESCE(direction,''),COALESCE(currency,''),COALESCE(amount_minor,0),category,merchant,occurred_at,timezone,COALESCE(time_precision,''),confidence,needs_clarification,status,created_at,updated_at FROM ledger_candidates`
const ledgerEntrySelect = `SELECT BIN_TO_UUID(id),BIN_TO_UUID(user_id),COALESCE(BIN_TO_UUID(candidate_id),''),idempotency_key,direction,currency,amount_minor,category,merchant,occurred_at,timezone,note,status,created_at,updated_at,deleted_at FROM ledger_entries`
const ledgerExportSelect = `SELECT BIN_TO_UUID(id),BIN_TO_UUID(user_id),month,currency,timezone,status,COALESCE(storage_key,''),COALESCE(file_name,''),COALESCE(media_type,''),COALESCE(size_bytes,0),COALESCE(sha256,''),COALESCE(failure_code,''),created_at,updated_at,completed_at,COALESCE(worker_id,'') FROM ledger_exports`

func scanLedgerCandidate(row rowScanner) (ledger.Candidate, error) {
	var item ledger.Candidate
	var occurred sql.NullTime
	var clarifications []byte
	err := row.Scan(&item.ID, &item.UserID, &item.SourceMessageID, &item.RawText, &item.Direction, &item.Currency, &item.AmountMinor, &item.Category, &item.Merchant, &occurred, &item.Timezone, &item.TimePrecision, &item.Confidence, &clarifications, &item.Status, &item.CreatedAt, &item.UpdatedAt)
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
	err := row.Scan(&item.ID, &item.UserID, &item.CandidateID, &item.IdempotencyKey, &item.Direction, &item.Currency, &item.AmountMinor, &item.Category, &item.Merchant, &item.OccurredAt, &item.Timezone, &item.Note, &item.Status, &item.CreatedAt, &item.UpdatedAt, &deleted)
	if deleted.Valid {
		item.DeletedAt = &deleted.Time
	}
	return item, err
}

func scanLedgerExport(row rowScanner) (ledger.ExportJob, error) {
	var job ledger.ExportJob
	var completed sql.NullTime
	err := row.Scan(&job.ID, &job.UserID, &job.Month, &job.Currency, &job.Timezone, &job.Status, &job.StorageKey, &job.FileName, &job.MediaType, &job.SizeBytes, &job.SHA256, &job.FailureCode, &job.CreatedAt, &job.UpdatedAt, &completed, &job.WorkerID)
	if completed.Valid {
		job.CompletedAt = &completed.Time
	}
	return job, err
}
