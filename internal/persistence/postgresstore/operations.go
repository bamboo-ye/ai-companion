package postgresstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/windcry1/ai-companion/internal/eventbus"
)

var _ eventbus.OperationsStore = (*Store)(nil)

func (s *Store) ListDeadLetterOutboxEvents(ctx context.Context, limit int) ([]eventbus.OutboxRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, outboxRecordSelect+`
		WHERE status='dead_letter'
		ORDER BY available_at,occurred_at
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := make([]eventbus.OutboxRecord, 0)
	for rows.Next() {
		record, scanErr := scanOutboxRecord(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) GetOutboxEvent(ctx context.Context, eventID string) (eventbus.OutboxRecord, error) {
	record, err := scanOutboxRecord(s.db.QueryRowContext(ctx, outboxRecordSelect+`
		WHERE id=$1`, eventID))
	if errors.Is(err, sql.ErrNoRows) {
		return eventbus.OutboxRecord{}, eventbus.ErrConflict
	}
	return record, err
}

func (s *Store) ListPoisonMessages(ctx context.Context, limit int) ([]eventbus.PoisonMessageRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, poisonMessageSelect+`
		ORDER BY observed_at DESC,id DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := make([]eventbus.PoisonMessageRecord, 0)
	for rows.Next() {
		record, scanErr := scanPoisonMessage(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) CreateCompensationRecord(ctx context.Context, input eventbus.CompensationInput) (eventbus.CompensationRecord, error) {
	if len(input.Metadata) == 0 {
		input.Metadata = json.RawMessage(`{}`)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO eventing.compensation_records
			(id,source_type,source_id,action,reason,actor,status,metadata,created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		input.ID, input.SourceType, input.SourceID, input.Action, input.Reason,
		input.Actor, input.Status, input.Metadata, input.CreatedAt,
	)
	if err != nil {
		return eventbus.CompensationRecord{}, err
	}
	return eventbus.CompensationRecord{
		ID: input.ID, SourceType: input.SourceType, SourceID: input.SourceID,
		Action: input.Action, Reason: input.Reason, Actor: input.Actor,
		Status: input.Status, Metadata: append([]byte(nil), input.Metadata...),
		CreatedAt: input.CreatedAt,
	}, nil
}

func (s *Store) ListCompensationRecords(ctx context.Context, limit int) ([]eventbus.CompensationRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, compensationRecordSelect+`
		ORDER BY created_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := make([]eventbus.CompensationRecord, 0)
	for rows.Next() {
		record, scanErr := scanCompensationRecord(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

const outboxRecordSelect = `
	SELECT id::text,aggregate_type,aggregate_id::text,event_type,event_version,
		payload,occurred_at,status,available_at,attempts,COALESCE(last_error,''),
		COALESCE(worker_id,''),lease_expires_at,published_at,
		COALESCE(published_topic,''),published_partition,published_offset
	FROM eventing.outbox_events`

func scanOutboxRecord(row rowScanner) (eventbus.OutboxRecord, error) {
	var record eventbus.OutboxRecord
	var payload []byte
	var lease, publishedAt sql.NullTime
	var partition, offset sql.NullInt64
	err := row.Scan(
		&record.ID, &record.AggregateType, &record.AggregateID, &record.Type,
		&record.Version, &payload, &record.OccurredAt, &record.Status,
		&record.AvailableAt, &record.Attempts, &record.LastError,
		&record.WorkerID, &lease, &publishedAt, &record.PublishedTopic,
		&partition, &offset,
	)
	record.Payload = append([]byte(nil), payload...)
	if lease.Valid {
		value := lease.Time
		record.LeaseExpiresAt = &value
	}
	if publishedAt.Valid {
		value := publishedAt.Time
		record.PublishedAt = &value
	}
	if partition.Valid {
		value := int(partition.Int64)
		record.PublishedPartition = &value
	}
	if offset.Valid {
		value := offset.Int64
		record.PublishedOffset = &value
	}
	return record, err
}

const poisonMessageSelect = `
	SELECT id,consumer_name,topic,partition_no,offset_no,COALESCE(event_id,''),
		COALESCE(event_type,''),COALESCE(aggregate_id,''),reason,
		COALESCE(envelope,'{}'::jsonb),observed_at
	FROM eventing.kafka_poison_messages`

func scanPoisonMessage(row rowScanner) (eventbus.PoisonMessageRecord, error) {
	var record eventbus.PoisonMessageRecord
	var envelope []byte
	err := row.Scan(
		&record.ID, &record.ConsumerName, &record.Topic, &record.Partition,
		&record.Offset, &record.EventID, &record.EventType, &record.AggregateID,
		&record.Reason, &envelope, &record.ObservedAt,
	)
	record.Envelope = append([]byte(nil), envelope...)
	return record, err
}

const compensationRecordSelect = `
	SELECT id::text,source_type,source_id,action,reason,actor,status,metadata,
		created_at,completed_at
	FROM eventing.compensation_records`

func scanCompensationRecord(row rowScanner) (eventbus.CompensationRecord, error) {
	var record eventbus.CompensationRecord
	var metadata []byte
	var completed sql.NullTime
	err := row.Scan(
		&record.ID, &record.SourceType, &record.SourceID, &record.Action,
		&record.Reason, &record.Actor, &record.Status, &metadata,
		&record.CreatedAt, &completed,
	)
	record.Metadata = append([]byte(nil), metadata...)
	if completed.Valid {
		value := completed.Time
		record.CompletedAt = &value
	}
	return record, err
}
