package mysqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/windcry1/ai-companion/internal/eventbus"
)

func (s *Store) ListDeadLetterOutboxEvents(ctx context.Context, limit int) ([]eventbus.OutboxRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, outboxRecordSelect+` WHERE status='dead_letter' ORDER BY available_at,occurred_at LIMIT ?`, limit)
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
	record, err := scanOutboxRecord(s.db.QueryRowContext(ctx, outboxRecordSelect+` WHERE id=UUID_TO_BIN(?)`, eventID))
	if errors.Is(err, sql.ErrNoRows) {
		return eventbus.OutboxRecord{}, eventbus.ErrConflict
	}
	return record, err
}

func (s *Store) CreateCompensationRecord(ctx context.Context, input eventbus.CompensationInput) (eventbus.CompensationRecord, error) {
	if len(input.Metadata) == 0 {
		input.Metadata = json.RawMessage(`{}`)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO compensation_records (id,source_type,source_id,action,reason,actor,status,metadata,created_at) VALUES (UUID_TO_BIN(?),?,?,?,?,?,?,?,?)`,
		input.ID, input.SourceType, input.SourceID, input.Action, input.Reason, input.Actor, input.Status, input.Metadata, input.CreatedAt)
	if err != nil {
		return eventbus.CompensationRecord{}, err
	}
	return eventbus.CompensationRecord{
		ID: input.ID, SourceType: input.SourceType, SourceID: input.SourceID, Action: input.Action, Reason: input.Reason,
		Actor: input.Actor, Status: input.Status, Metadata: append([]byte(nil), input.Metadata...), CreatedAt: input.CreatedAt,
	}, nil
}

func (s *Store) ListCompensationRecords(ctx context.Context, limit int) ([]eventbus.CompensationRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, compensationRecordSelect+` ORDER BY created_at DESC LIMIT ?`, limit)
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

const outboxRecordSelect = `SELECT BIN_TO_UUID(id),aggregate_type,BIN_TO_UUID(aggregate_id),event_type,event_version,payload,occurred_at,status,available_at,attempts,COALESCE(last_error,''),COALESCE(worker_id,''),lease_expires_at,published_at,COALESCE(published_topic,''),published_partition,published_offset FROM outbox_events`

func scanOutboxRecord(row rowScanner) (eventbus.OutboxRecord, error) {
	var record eventbus.OutboxRecord
	var payload []byte
	var lease, publishedAt sql.NullTime
	var partition sql.NullInt64
	var offset sql.NullInt64
	if err := row.Scan(&record.ID, &record.AggregateType, &record.AggregateID, &record.Type, &record.Version, &payload, &record.OccurredAt, &record.Status, &record.AvailableAt, &record.Attempts, &record.LastError, &record.WorkerID, &lease, &publishedAt, &record.PublishedTopic, &partition, &offset); err != nil {
		return eventbus.OutboxRecord{}, err
	}
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
	return record, nil
}

const compensationRecordSelect = `SELECT BIN_TO_UUID(id),source_type,source_id,action,reason,actor,status,metadata,created_at,completed_at FROM compensation_records`

func scanCompensationRecord(row rowScanner) (eventbus.CompensationRecord, error) {
	var record eventbus.CompensationRecord
	var metadata []byte
	var completed sql.NullTime
	if err := row.Scan(&record.ID, &record.SourceType, &record.SourceID, &record.Action, &record.Reason, &record.Actor, &record.Status, &metadata, &record.CreatedAt, &completed); err != nil {
		return eventbus.CompensationRecord{}, err
	}
	record.Metadata = append([]byte(nil), metadata...)
	if completed.Valid {
		value := completed.Time
		record.CompletedAt = &value
	}
	return record, nil
}
