package mysqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/windcry1/ai-companion/internal/email"
	"github.com/windcry1/ai-companion/internal/platform/id"
)

func (s *Store) CreateDelivery(ctx context.Context, item email.Delivery) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO email_deliveries (id,actor_id,resource_type,resource_id,template,recipient_email,subject,body_text,status,available_at,created_at,updated_at) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(NULLIF(?,'')),?,UUID_TO_BIN(?),?,?,?,?,?,?,?,?)`,
		item.ID, item.ActorID, item.ResourceType, item.ResourceID, item.Template, item.RecipientEmail, item.Subject, item.BodyText, item.Status, item.AvailableAt, item.CreatedAt, item.UpdatedAt)
	if isDuplicate(err) {
		return email.ErrConflict
	}
	if err != nil {
		return err
	}
	metadata, _ := json.Marshal(map[string]any{"recipient_email": item.RecipientEmail, "template": item.Template, "resource_type": item.ResourceType, "resource_id": item.ResourceID})
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_logs (actor_type,actor_id,action,resource_type,resource_id,metadata,occurred_at) VALUES ('user',UUID_TO_BIN(NULLIF(?,'')),'email.delivery.queued','email_delivery',UUID_TO_BIN(?),?,?)`, item.ActorID, item.ID, metadata, item.CreatedAt); err != nil {
		return err
	}
	eventID, err := id.New()
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"delivery_id": item.ID})
	if _, err = tx.ExecContext(ctx, `INSERT INTO outbox_events (id,aggregate_type,aggregate_id,event_type,event_version,payload,occurred_at) VALUES (UUID_TO_BIN(?),'email_delivery',UUID_TO_BIN(?),'email.deliver.v1',1,?,?)`, eventID, item.ID, payload, item.CreatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListDeliveries(ctx context.Context, status string, limit int) ([]email.Delivery, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := emailDeliverySelect
	args := []any{}
	if status != "" {
		query += ` WHERE status=?`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]email.Delivery, 0)
	for rows.Next() {
		item, scanErr := scanEmailDelivery(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetDelivery(ctx context.Context, deliveryID string) (email.Delivery, error) {
	item, err := scanEmailDelivery(s.db.QueryRowContext(ctx, emailDeliverySelect+` WHERE id=UUID_TO_BIN(?)`, deliveryID))
	if errors.Is(err, sql.ErrNoRows) {
		return email.Delivery{}, email.ErrNotFound
	}
	return item, err
}

func (s *Store) ListDeliveriesByResource(ctx context.Context, resourceType, resourceID string) ([]email.Delivery, error) {
	rows, err := s.db.QueryContext(ctx, emailDeliverySelect+` WHERE resource_type=? AND resource_id=UUID_TO_BIN(?) ORDER BY created_at DESC`, resourceType, resourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]email.Delivery, 0)
	for rows.Next() {
		item, scanErr := scanEmailDelivery(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ReplayDelivery(ctx context.Context, deliveryID string, now time.Time) (email.Delivery, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return email.Delivery{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE email_deliveries SET status='queued',failure_code='',last_error='',worker_id=NULL,lease_expires_at=NULL,available_at=?,updated_at=? WHERE id=UUID_TO_BIN(?) AND status='failed'`, now, now, deliveryID)
	if err != nil {
		return email.Delivery{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		if _, getErr := scanEmailDelivery(tx.QueryRowContext(ctx, emailDeliverySelect+` WHERE id=UUID_TO_BIN(?)`, deliveryID)); errors.Is(getErr, sql.ErrNoRows) {
			return email.Delivery{}, email.ErrNotFound
		}
		return email.Delivery{}, email.ErrConflict
	}
	eventID, err := id.New()
	if err != nil {
		return email.Delivery{}, err
	}
	payload, _ := json.Marshal(map[string]string{"delivery_id": deliveryID})
	if _, err = tx.ExecContext(ctx, `INSERT INTO outbox_events (id,aggregate_type,aggregate_id,event_type,event_version,payload,occurred_at) VALUES (UUID_TO_BIN(?),'email_delivery',UUID_TO_BIN(?),'email.deliver.v1',1,?,?)`, eventID, deliveryID, payload, now); err != nil {
		return email.Delivery{}, err
	}
	metadata, _ := json.Marshal(map[string]any{"result": "replayed_to_queued"})
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_logs (actor_type,actor_id,action,resource_type,resource_id,metadata,occurred_at) VALUES ('system',NULL,'email.delivery.replayed','email_delivery',UUID_TO_BIN(?),?,?)`, deliveryID, metadata, now); err != nil {
		return email.Delivery{}, err
	}
	item, err := scanEmailDelivery(tx.QueryRowContext(ctx, emailDeliverySelect+` WHERE id=UUID_TO_BIN(?)`, deliveryID))
	if err != nil {
		return email.Delivery{}, err
	}
	if err = tx.Commit(); err != nil {
		return email.Delivery{}, err
	}
	return item, nil
}

func (s *Store) ClaimDelivery(ctx context.Context, workerID string, now time.Time, lease time.Duration) (email.Delivery, error) {
	return s.claimDelivery(ctx, `1=1`, nil, workerID, now, lease)
}

func (s *Store) ClaimDeliveryByID(ctx context.Context, deliveryID, workerID string, now time.Time, lease time.Duration) (email.Delivery, error) {
	return s.claimDelivery(ctx, `id=UUID_TO_BIN(?)`, []any{deliveryID}, workerID, now, lease)
}

func (s *Store) claimDelivery(ctx context.Context, predicate string, args []any, workerID string, now time.Time, lease time.Duration) (email.Delivery, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return email.Delivery{}, err
	}
	defer tx.Rollback()
	queryArgs := append(append([]any{}, args...), now, now)
	item, err := scanEmailDelivery(tx.QueryRowContext(ctx, emailDeliverySelect+` WHERE `+predicate+` AND available_at<=? AND (status='queued' OR (status='processing' AND lease_expires_at<=?)) ORDER BY available_at,created_at LIMIT 1 FOR UPDATE SKIP LOCKED`, queryArgs...))
	if errors.Is(err, sql.ErrNoRows) {
		return email.Delivery{}, email.ErrNoDelivery
	}
	if err != nil {
		return email.Delivery{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE email_deliveries SET status='processing',attempts=attempts+1,worker_id=?,lease_expires_at=?,updated_at=? WHERE id=UUID_TO_BIN(?)`, workerID, now.Add(lease), now, item.ID); err != nil {
		return email.Delivery{}, err
	}
	if err = tx.Commit(); err != nil {
		return email.Delivery{}, err
	}
	item.Status, item.WorkerID, item.Attempts, item.UpdatedAt = "processing", workerID, item.Attempts+1, now
	expires := now.Add(lease)
	item.LeaseExpiresAt = &expires
	return item, nil
}

func (s *Store) CompleteDelivery(ctx context.Context, item email.Delivery, workerID, providerMessageID string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE email_deliveries SET status='sent',provider='smtp',provider_message_id=?,failure_code='',last_error='',worker_id=NULL,lease_expires_at=NULL,updated_at=?,sent_at=? WHERE id=UUID_TO_BIN(?) AND status='processing' AND worker_id=?`, providerMessageID, now, now, item.ID, workerID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return email.ErrConflict
	}
	metadata, _ := json.Marshal(map[string]any{"provider_message_id": providerMessageID, "recipient_email": item.RecipientEmail, "template": item.Template})
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_logs (actor_type,actor_id,action,resource_type,resource_id,metadata,occurred_at) VALUES ('system',NULL,'email.delivery.sent','email_delivery',UUID_TO_BIN(?),?,?)`, item.ID, metadata, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FailDelivery(ctx context.Context, item email.Delivery, workerID, code string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE email_deliveries SET status='failed',failure_code=?,last_error=?,worker_id=NULL,lease_expires_at=NULL,updated_at=? WHERE id=UUID_TO_BIN(?) AND status='processing' AND worker_id=?`, code, code, now, item.ID, workerID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return email.ErrConflict
	}
	metadata, _ := json.Marshal(map[string]any{"failure_code": code, "recipient_email": item.RecipientEmail, "template": item.Template})
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_logs (actor_type,actor_id,action,resource_type,resource_id,metadata,occurred_at) VALUES ('system',NULL,'email.delivery.failed','email_delivery',UUID_TO_BIN(?),?,?)`, item.ID, metadata, now); err != nil {
		return err
	}
	return tx.Commit()
}

const emailDeliverySelect = `SELECT BIN_TO_UUID(id),COALESCE(BIN_TO_UUID(actor_id),''),resource_type,BIN_TO_UUID(resource_id),template,recipient_email,subject,body_text,status,COALESCE(provider,''),COALESCE(provider_message_id,''),COALESCE(failure_code,''),attempts,available_at,COALESCE(worker_id,''),lease_expires_at,created_at,updated_at,sent_at FROM email_deliveries`

func scanEmailDelivery(row rowScanner) (email.Delivery, error) {
	var item email.Delivery
	var lease, sent sql.NullTime
	if err := row.Scan(&item.ID, &item.ActorID, &item.ResourceType, &item.ResourceID, &item.Template, &item.RecipientEmail, &item.Subject, &item.BodyText, &item.Status, &item.Provider, &item.ProviderMessageID, &item.FailureCode, &item.Attempts, &item.AvailableAt, &item.WorkerID, &lease, &item.CreatedAt, &item.UpdatedAt, &sent); err != nil {
		return email.Delivery{}, err
	}
	if lease.Valid {
		item.LeaseExpiresAt = &lease.Time
	}
	if sent.Valid {
		item.SentAt = &sent.Time
	}
	return item, nil
}
