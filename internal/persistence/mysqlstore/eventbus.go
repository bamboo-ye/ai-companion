package mysqlstore

import (
	"context"
	"time"

	"github.com/windcry1/ai-companion/internal/eventbus"
)

func (s *Store) ClaimOutboxEvents(ctx context.Context, workerID string, now time.Time, lease time.Duration, limit int) ([]eventbus.Event, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT BIN_TO_UUID(id),aggregate_type,BIN_TO_UUID(aggregate_id),event_type,event_version,payload,occurred_at,attempts FROM outbox_events WHERE available_at<=? AND (status='pending' OR (status='publishing' AND lease_expires_at<=?)) ORDER BY available_at,occurred_at LIMIT ? FOR UPDATE SKIP LOCKED`, now, now, limit)
	if err != nil {
		return nil, err
	}
	events := make([]eventbus.Event, 0)
	for rows.Next() {
		var event eventbus.Event
		var payload []byte
		if err = rows.Scan(&event.ID, &event.AggregateType, &event.AggregateID, &event.Type, &event.Version, &payload, &event.OccurredAt, &event.Attempts); err != nil {
			rows.Close()
			return nil, err
		}
		event.Payload = append([]byte(nil), payload...)
		event.Attempts++
		events = append(events, event)
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, eventbus.ErrNoEvents
	}
	expires := now.Add(lease)
	for _, event := range events {
		result, updateErr := tx.ExecContext(ctx, `UPDATE outbox_events SET status='publishing',attempts=attempts+1,worker_id=?,lease_expires_at=?,last_error=NULL WHERE id=UUID_TO_BIN(?) AND status IN ('pending','publishing')`, workerID, expires, event.ID)
		if updateErr != nil {
			return nil, updateErr
		}
		affected, _ := result.RowsAffected()
		if affected != 1 {
			return nil, eventbus.ErrConflict
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return events, nil
}

func (s *Store) MarkOutboxPublished(ctx context.Context, eventID, workerID string, ack eventbus.PublishAck, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE outbox_events SET status='published',published_at=?,worker_id=NULL,lease_expires_at=NULL,published_topic=?,published_partition=?,published_offset=?,last_error=NULL WHERE id=UUID_TO_BIN(?) AND status='publishing' AND worker_id=?`, now, ack.Topic, ack.Partition, ack.Offset, eventID, workerID)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return eventbus.ErrConflict
	}
	return nil
}

func (s *Store) FailOutboxEvent(ctx context.Context, eventID, workerID, message string, availableAt time.Time, dead bool) error {
	status := "pending"
	if dead {
		status = "dead_letter"
	}
	result, err := s.db.ExecContext(ctx, `UPDATE outbox_events SET status=?,available_at=?,worker_id=NULL,lease_expires_at=NULL,last_error=? WHERE id=UUID_TO_BIN(?) AND status='publishing' AND worker_id=?`, status, availableAt, message, eventID, workerID)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return eventbus.ErrConflict
	}
	return nil
}

func (s *Store) RecordInboxEvent(ctx context.Context, consumer, eventID string, now time.Time) (bool, error) {
	_, err := s.db.ExecContext(ctx, `INSERT INTO inbox_events (consumer_name,event_id,processed_at) VALUES (?,UUID_TO_BIN(?),?)`, consumer, eventID, now)
	if isDuplicate(err) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) InboxEventExists(ctx context.Context, consumer, eventID string) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM inbox_events WHERE consumer_name=? AND event_id=UUID_TO_BIN(?)`, consumer, eventID).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *Store) ReplayOutboxEvent(ctx context.Context, eventID string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE outbox_events SET status='pending',available_at=?,attempts=0,worker_id=NULL,lease_expires_at=NULL,last_error=NULL,published_at=NULL,published_topic=NULL,published_partition=NULL,published_offset=NULL WHERE id=UUID_TO_BIN(?) AND status='dead_letter'`, now, eventID)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return eventbus.ErrConflict
	}
	return nil
}
