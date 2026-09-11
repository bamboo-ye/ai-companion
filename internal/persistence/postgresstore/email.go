package postgresstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/windcry1/ai-companion/internal/email"
	"github.com/windcry1/ai-companion/internal/platform/id"
)

var _ email.Store = (*Store)(nil)

func (s *Store) CreateDelivery(ctx context.Context, item email.Delivery) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO app.email_deliveries (
			id,actor_id,resource_type,resource_id,template,recipient_email,
			subject,body_text,status,available_at,created_at,updated_at
		) VALUES (
			$1,NULLIF($2,'')::uuid,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12
		)`,
		item.ID, item.ActorID, item.ResourceType, item.ResourceID, item.Template,
		item.RecipientEmail, item.Subject, item.BodyText, item.Status,
		item.AvailableAt, item.CreatedAt, item.UpdatedAt,
	)
	if isUniqueViolation(err) {
		return email.ErrConflict
	}
	if err != nil {
		return err
	}
	metadata, _ := json.Marshal(map[string]any{
		"recipient_email": item.RecipientEmail,
		"template":        item.Template,
		"resource_type":   item.ResourceType,
		"resource_id":     item.ResourceID,
	})
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO eventing.audit_logs
			(actor_type,actor_id,action,resource_type,resource_id,metadata,occurred_at)
		VALUES ('user',NULLIF($1,'')::uuid,'email.delivery.queued','email_delivery',$2,$3,$4)`,
		item.ActorID, item.ID, metadata, item.CreatedAt,
	); err != nil {
		return err
	}
	eventID, err := id.New()
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"delivery_id": item.ID})
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO eventing.outbox_events
			(id,aggregate_type,aggregate_id,event_type,event_version,payload,occurred_at,trace_id,traceparent,tracestate)
		VALUES ($1,'email_delivery',$2,'email.deliver.v1',1,$3,$4,NULLIF($5,''),NULLIF($6,''),NULLIF($7,''))`,
		eventID, item.ID, payload, item.CreatedAt, outboxTraceID(ctx), outboxTraceParent(ctx), outboxTraceState(ctx),
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListDeliveries(ctx context.Context, status string, limit int) ([]email.Delivery, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := emailDeliverySelect
	args := make([]any, 0, 2)
	if status != "" {
		args = append(args, status)
		query += ` WHERE status=$1`
	}
	args = append(args, limit)
	query += ` ORDER BY created_at DESC LIMIT $` + placeholder(len(args))
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
	item, err := scanEmailDelivery(s.db.QueryRowContext(ctx, emailDeliverySelect+` WHERE id=$1`, deliveryID))
	if errors.Is(err, sql.ErrNoRows) {
		return email.Delivery{}, email.ErrNotFound
	}
	return item, err
}

func (s *Store) ListDeliveriesByResource(ctx context.Context, resourceType, resourceID string) ([]email.Delivery, error) {
	rows, err := s.db.QueryContext(ctx, emailDeliverySelect+`
		WHERE resource_type=$1 AND resource_id=$2
		ORDER BY created_at DESC`,
		resourceType, resourceID,
	)
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
	result, err := tx.ExecContext(ctx, `
		UPDATE app.email_deliveries SET
			status='queued',failure_code='',last_error='',worker_id=NULL,
			lease_expires_at=NULL,available_at=$1,updated_at=$1
		WHERE id=$2 AND status='failed'`,
		now, deliveryID,
	)
	if err != nil {
		return email.Delivery{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		if _, getErr := scanEmailDelivery(tx.QueryRowContext(ctx, emailDeliverySelect+` WHERE id=$1`, deliveryID)); errors.Is(getErr, sql.ErrNoRows) {
			return email.Delivery{}, email.ErrNotFound
		}
		return email.Delivery{}, email.ErrConflict
	}
	eventID, err := id.New()
	if err != nil {
		return email.Delivery{}, err
	}
	payload, _ := json.Marshal(map[string]string{"delivery_id": deliveryID})
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO eventing.outbox_events
			(id,aggregate_type,aggregate_id,event_type,event_version,payload,occurred_at,trace_id,traceparent,tracestate)
		VALUES ($1,'email_delivery',$2,'email.deliver.v1',1,$3,$4,NULLIF($5,''),NULLIF($6,''),NULLIF($7,''))`,
		eventID, deliveryID, payload, now, outboxTraceID(ctx), outboxTraceParent(ctx), outboxTraceState(ctx),
	); err != nil {
		return email.Delivery{}, err
	}
	metadata, _ := json.Marshal(map[string]any{"result": "replayed_to_queued"})
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO eventing.audit_logs
			(actor_type,actor_id,action,resource_type,resource_id,metadata,occurred_at)
		VALUES ('system',NULL,'email.delivery.replayed','email_delivery',$1,$2,$3)`,
		deliveryID, metadata, now,
	); err != nil {
		return email.Delivery{}, err
	}
	item, err := scanEmailDelivery(tx.QueryRowContext(ctx, emailDeliverySelect+` WHERE id=$1`, deliveryID))
	if err != nil {
		return email.Delivery{}, err
	}
	if err = tx.Commit(); err != nil {
		return email.Delivery{}, err
	}
	return item, nil
}

func (s *Store) ClaimDelivery(ctx context.Context, workerID string, now time.Time, lease time.Duration) (email.Delivery, error) {
	return s.claimDelivery(ctx, "", workerID, now, lease)
}

func (s *Store) ClaimDeliveryByID(ctx context.Context, deliveryID, workerID string, now time.Time, lease time.Duration) (email.Delivery, error) {
	return s.claimDelivery(ctx, deliveryID, workerID, now, lease)
}

func (s *Store) claimDelivery(ctx context.Context, deliveryID, workerID string, now time.Time, lease time.Duration) (email.Delivery, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return email.Delivery{}, err
	}
	defer tx.Rollback()
	item, err := scanEmailDelivery(tx.QueryRowContext(ctx, emailDeliverySelect+`
		WHERE ($1='' OR id=$1::uuid)
			AND available_at<=$2
			AND (status='queued' OR (status='processing' AND lease_expires_at<=$2))
		ORDER BY available_at,created_at
		LIMIT 1
		FOR UPDATE SKIP LOCKED`,
		deliveryID, now,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return email.Delivery{}, email.ErrNoDelivery
	}
	if err != nil {
		return email.Delivery{}, err
	}
	expires := now.Add(lease)
	if _, err = tx.ExecContext(ctx, `
		UPDATE app.email_deliveries SET
			status='processing',attempts=attempts+1,worker_id=$1,
			lease_expires_at=$2,updated_at=$3
		WHERE id=$4`,
		workerID, expires, now, item.ID,
	); err != nil {
		return email.Delivery{}, err
	}
	if err = tx.Commit(); err != nil {
		return email.Delivery{}, err
	}
	item.Status = "processing"
	item.WorkerID = workerID
	item.Attempts++
	item.UpdatedAt = now
	item.LeaseExpiresAt = &expires
	return item, nil
}

func (s *Store) CompleteDelivery(ctx context.Context, item email.Delivery, workerID, providerMessageID string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE app.email_deliveries SET
			status='sent',provider='smtp',provider_message_id=$1,
			failure_code='',last_error='',worker_id=NULL,lease_expires_at=NULL,
			updated_at=$2,sent_at=$2
		WHERE id=$3 AND status='processing' AND worker_id=$4`,
		providerMessageID, now, item.ID, workerID,
	)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return email.ErrConflict
	}
	metadata, _ := json.Marshal(map[string]any{
		"provider_message_id": providerMessageID,
		"recipient_email":     item.RecipientEmail,
		"template":            item.Template,
	})
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO eventing.audit_logs
			(actor_type,actor_id,action,resource_type,resource_id,metadata,occurred_at)
		VALUES ('system',NULL,'email.delivery.sent','email_delivery',$1,$2,$3)`,
		item.ID, metadata, now,
	); err != nil {
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
	result, err := tx.ExecContext(ctx, `
		UPDATE app.email_deliveries SET
			status='failed',failure_code=$1,last_error=$1,worker_id=NULL,
			lease_expires_at=NULL,updated_at=$2
		WHERE id=$3 AND status='processing' AND worker_id=$4`,
		code, now, item.ID, workerID,
	)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return email.ErrConflict
	}
	metadata, _ := json.Marshal(map[string]any{
		"failure_code":    code,
		"recipient_email": item.RecipientEmail,
		"template":        item.Template,
	})
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO eventing.audit_logs
			(actor_type,actor_id,action,resource_type,resource_id,metadata,occurred_at)
		VALUES ('system',NULL,'email.delivery.failed','email_delivery',$1,$2,$3)`,
		item.ID, metadata, now,
	); err != nil {
		return err
	}
	return tx.Commit()
}

const emailDeliverySelect = `
	SELECT id::text,COALESCE(actor_id::text,''),resource_type,resource_id::text,
		template,recipient_email,subject,body_text,status,provider,
		provider_message_id,failure_code,attempts,available_at,
		COALESCE(worker_id,''),lease_expires_at,created_at,updated_at,sent_at
	FROM app.email_deliveries`

func scanEmailDelivery(row rowScanner) (email.Delivery, error) {
	var item email.Delivery
	var lease, sent sql.NullTime
	err := row.Scan(
		&item.ID, &item.ActorID, &item.ResourceType, &item.ResourceID,
		&item.Template, &item.RecipientEmail, &item.Subject, &item.BodyText,
		&item.Status, &item.Provider, &item.ProviderMessageID, &item.FailureCode,
		&item.Attempts, &item.AvailableAt, &item.WorkerID, &lease,
		&item.CreatedAt, &item.UpdatedAt, &sent,
	)
	if lease.Valid {
		value := lease.Time
		item.LeaseExpiresAt = &value
	}
	if sent.Valid {
		value := sent.Time
		item.SentAt = &value
	}
	return item, err
}
