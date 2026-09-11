package postgresstore

import (
	"context"
	"encoding/json"
	"time"

	"github.com/windcry1/ai-companion/internal/eventbus"
	"github.com/windcry1/ai-companion/internal/platform/tracectx"
)

var _ eventbus.Store = (*Store)(nil)

func (s *Store) ClaimOutboxEvents(ctx context.Context, workerID string, now time.Time, lease time.Duration, limit int) ([]eventbus.Event, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
		SELECT
			id::text,aggregate_type,aggregate_id::text,event_type,event_version,
			payload,occurred_at,attempts,COALESCE(trace_id,''),
			COALESCE(traceparent,''),COALESCE(tracestate,'')
		FROM eventing.outbox_events
		WHERE available_at<=$1
			AND (
				status='pending'
				OR (status='publishing' AND lease_expires_at<=$1)
			)
		ORDER BY available_at,occurred_at
		LIMIT $2
		FOR UPDATE SKIP LOCKED`,
		now, limit,
	)
	if err != nil {
		return nil, err
	}
	events := make([]eventbus.Event, 0)
	for rows.Next() {
		var event eventbus.Event
		var payload []byte
		if err = rows.Scan(
			&event.ID, &event.AggregateType, &event.AggregateID, &event.Type,
			&event.Version, &payload, &event.OccurredAt, &event.Attempts, &event.TraceID,
			&event.TraceParent, &event.TraceState,
		); err != nil {
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
		result, updateErr := tx.ExecContext(ctx, `
			UPDATE eventing.outbox_events SET
				status='publishing',
				attempts=attempts+1,
				worker_id=$1,
				lease_expires_at=$2,
				last_error=NULL
			WHERE id=$3 AND status IN ('pending','publishing')`,
			workerID, expires, event.ID,
		)
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

func outboxTraceID(ctx context.Context) string { return tracectx.ID(ctx) }

func outboxTraceParent(ctx context.Context) string { return tracectx.TraceParent(ctx) }

func outboxTraceState(ctx context.Context) string { return tracectx.TraceState(ctx) }

func (s *Store) MarkOutboxPublished(ctx context.Context, eventID, workerID string, ack eventbus.PublishAck, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE eventing.outbox_events SET
			status='published',
			published_at=$1,
			worker_id=NULL,
			lease_expires_at=NULL,
			published_topic=$2,
			published_partition=$3,
			published_offset=$4,
			last_error=NULL
		WHERE id=$5 AND status='publishing' AND worker_id=$6`,
		now, ack.Topic, ack.Partition, ack.Offset, eventID, workerID,
	)
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
	result, err := s.db.ExecContext(ctx, `
		UPDATE eventing.outbox_events SET
			status=$1,
			available_at=$2,
			worker_id=NULL,
			lease_expires_at=NULL,
			last_error=$3
		WHERE id=$4 AND status='publishing' AND worker_id=$5`,
		status, availableAt, message, eventID, workerID,
	)
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
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO eventing.inbox_events (consumer_name,event_id,processed_at)
		VALUES ($1,$2,$3)
		ON CONFLICT (consumer_name,event_id) DO NOTHING`,
		consumer, eventID, now,
	)
	if err != nil {
		return false, err
	}
	affected, _ := result.RowsAffected()
	return affected == 1, nil
}

func (s *Store) InboxEventExists(ctx context.Context, consumer, eventID string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM eventing.inbox_events
			WHERE consumer_name=$1 AND event_id=$2
		)`,
		consumer, eventID,
	).Scan(&exists)
	return exists, err
}

func (s *Store) RecordPoisonMessage(ctx context.Context, input eventbus.PoisonMessageInput) error {
	var envelope any
	if len(input.Envelope) > 0 {
		envelope = json.RawMessage(input.Envelope)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO eventing.kafka_poison_messages (
			consumer_name,topic,partition_no,offset_no,event_id,event_type,
			aggregate_id,reason,envelope,observed_at
		) VALUES (
			$1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),NULLIF($7,''),$8,$9,$10
		)
		ON CONFLICT (consumer_name,topic,partition_no,offset_no) DO UPDATE SET
			reason=EXCLUDED.reason,
			event_id=EXCLUDED.event_id,
			event_type=EXCLUDED.event_type,
			aggregate_id=EXCLUDED.aggregate_id,
			envelope=EXCLUDED.envelope,
			observed_at=EXCLUDED.observed_at`,
		input.ConsumerName, input.Topic, input.Partition, input.Offset,
		input.EventID, input.EventType, input.AggregateID, input.Reason,
		envelope, input.ObservedAt,
	)
	return err
}

func (s *Store) ReplayOutboxEvent(ctx context.Context, eventID string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE eventing.outbox_events SET
			status='pending',
			available_at=$1,
			attempts=0,
			worker_id=NULL,
			lease_expires_at=NULL,
			last_error=NULL,
			published_at=NULL,
			published_topic=NULL,
			published_partition=NULL,
			published_offset=NULL
		WHERE id=$2 AND status='dead_letter'`,
		now, eventID,
	)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return eventbus.ErrConflict
	}
	return nil
}
