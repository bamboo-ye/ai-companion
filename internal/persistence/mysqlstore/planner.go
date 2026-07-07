package mysqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/windcry1/ai-companion/internal/planner"
	"github.com/windcry1/ai-companion/internal/platform/id"
)

func (s *Store) CreatePlan(ctx context.Context, item planner.Plan) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO plans (id,user_id,title,local_date,timezone,status,created_at,updated_at) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),?,?,?,?,?,?)`, item.ID, item.UserID, item.Title, item.LocalDate, item.Timezone, item.Status, item.CreatedAt, item.UpdatedAt); err != nil {
		return err
	}
	for _, planItem := range item.Items {
		if _, err = tx.ExecContext(ctx, `INSERT INTO plan_items (id,plan_id,title,priority,estimated_minutes,starts_at,ends_at,location,status,source,created_at,updated_at) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),?,?,?,?,?,?,?,?,?,?)`, planItem.ID, item.ID, planItem.Title, planItem.Priority, planItem.EstimatedMinute, planItem.StartsAt, planItem.EndsAt, planItem.Location, planItem.Status, planItem.Source, planItem.CreatedAt, planItem.UpdatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListPlans(ctx context.Context, userID, localDate string) ([]planner.Plan, error) {
	query := planSelect + ` WHERE user_id=UUID_TO_BIN(?) AND status='active'`
	args := []any{userID}
	if localDate != "" {
		query += ` AND local_date=?`
		args = append(args, localDate)
	}
	query += ` ORDER BY local_date,created_at`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	plans := make([]planner.Plan, 0)
	for rows.Next() {
		item, scanErr := scanPlan(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		plans = append(plans, item)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for index := range plans {
		itemRows, itemErr := s.db.QueryContext(ctx, planItemSelect+` WHERE plan_id=UUID_TO_BIN(?) ORDER BY starts_at IS NULL,starts_at,created_at`, plans[index].ID)
		if itemErr != nil {
			return nil, itemErr
		}
		for itemRows.Next() {
			planItem, scanErr := scanPlanItem(itemRows)
			if scanErr != nil {
				itemRows.Close()
				return nil, scanErr
			}
			plans[index].Items = append(plans[index].Items, planItem)
		}
		if itemErr = itemRows.Close(); itemErr != nil {
			return nil, itemErr
		}
	}
	return plans, nil
}

func (s *Store) CreateReminder(ctx context.Context, item planner.Reminder) error {
	clarifications, err := json.Marshal(item.NeedsClarification)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO reminders (id,user_id,plan_item_id,source_message_id,raw_text,title,due_at,local_due,timezone,time_precision,recurrence,needs_clarification,status,system_sync_status,created_at,updated_at) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),UUID_TO_BIN(NULLIF(?,'')),UUID_TO_BIN(NULLIF(?,'')),?,?,?,?,?,NULLIF(?,''),?,?,?,?,?,?)`, item.ID, item.UserID, item.PlanItemID, item.SourceMessageID, item.RawText, item.Title, item.DueAt, item.LocalDue, item.Timezone, item.TimePrecision, item.Recurrence, clarifications, item.Status, item.SystemSyncStatus, item.CreatedAt, item.UpdatedAt)
	return err
}

func (s *Store) GetReminder(ctx context.Context, userID, reminderID string) (planner.Reminder, error) {
	item, err := scanReminder(s.db.QueryRowContext(ctx, reminderSelect+` WHERE r.id=UUID_TO_BIN(?) AND r.user_id=UUID_TO_BIN(?) AND r.status<>'cancelled'`, reminderID, userID))
	if errors.Is(err, sql.ErrNoRows) {
		err = planner.ErrNotFound
	}
	return item, err
}

func (s *Store) ConfirmReminder(ctx context.Context, item planner.Reminder, key string, now time.Time) (planner.Reminder, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return planner.Reminder{}, false, err
	}
	defer tx.Rollback()
	locked, err := scanReminder(tx.QueryRowContext(ctx, reminderSelect+` WHERE r.id=UUID_TO_BIN(?) AND r.user_id=UUID_TO_BIN(?) FOR UPDATE`, item.ID, item.UserID))
	if errors.Is(err, sql.ErrNoRows) {
		return planner.Reminder{}, false, planner.ErrNotFound
	}
	if err != nil {
		return planner.Reminder{}, false, err
	}
	var owner string
	err = tx.QueryRowContext(ctx, `SELECT BIN_TO_UUID(id) FROM reminders WHERE user_id=UUID_TO_BIN(?) AND confirmation_key=? LIMIT 1`, item.UserID, key).Scan(&owner)
	if err == nil {
		if owner != item.ID {
			return planner.Reminder{}, false, planner.ErrIdempotencyReuse
		}
		return locked, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return planner.Reminder{}, false, err
	}
	if locked.Status == "active" || locked.Status == "completed" {
		return locked, false, nil
	}
	if locked.Status != "pending_confirmation" || locked.DueAt == nil || len(locked.NeedsClarification) > 0 {
		return planner.Reminder{}, false, planner.ErrConfirmation
	}
	if _, err = tx.ExecContext(ctx, `UPDATE reminders SET status='active',confirmation_key=?,updated_at=? WHERE id=UUID_TO_BIN(?)`, key, now, item.ID); err != nil {
		return planner.Reminder{}, false, err
	}
	data, _ := json.Marshal(map[string]any{"due_at": locked.DueAt, "timezone": locked.Timezone})
	if _, err = tx.ExecContext(ctx, `INSERT INTO reminder_events (reminder_id,user_id,event_type,event_data,occurred_at) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),'confirmed',?,?)`, item.ID, item.UserID, data, now); err != nil {
		return planner.Reminder{}, false, err
	}
	deliveryID, err := id.New()
	if err != nil {
		return planner.Reminder{}, false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO notification_deliveries (id,reminder_id,user_id,channel,scheduled_at,status,created_at,updated_at) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),UUID_TO_BIN(?),'in_app',?,'queued',?,?)`, deliveryID, item.ID, item.UserID, *locked.DueAt, now, now); err != nil {
		return planner.Reminder{}, false, err
	}
	metadata, _ := json.Marshal(map[string]string{"timezone": locked.Timezone, "local_due": locked.LocalDue})
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_logs (actor_type,actor_id,action,resource_type,resource_id,metadata,occurred_at) VALUES ('user',UUID_TO_BIN(?),'reminder.confirm','reminder',UUID_TO_BIN(?),?,?)`, item.UserID, item.ID, metadata, now); err != nil {
		return planner.Reminder{}, false, err
	}
	eventID, err := id.New()
	if err != nil {
		return planner.Reminder{}, false, err
	}
	payload, _ := json.Marshal(map[string]string{"reminder_id": item.ID, "user_id": item.UserID})
	if _, err = tx.ExecContext(ctx, `INSERT INTO outbox_events (id,aggregate_type,aggregate_id,event_type,event_version,payload,occurred_at) VALUES (UUID_TO_BIN(?),'reminder',UUID_TO_BIN(?),'reminder.confirmed.v1',1,?,?)`, eventID, item.ID, payload, now); err != nil {
		return planner.Reminder{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return planner.Reminder{}, false, err
	}
	locked.Status = "active"
	locked.UpdatedAt = now
	return locked, true, nil
}

func (s *Store) ListReminders(ctx context.Context, userID string, start, end *time.Time, limit int) ([]planner.Reminder, error) {
	query := reminderSelect + ` WHERE r.user_id=UUID_TO_BIN(?) AND r.status<>'cancelled'`
	args := []any{userID}
	if start != nil {
		query += ` AND r.due_at>=?`
		args = append(args, *start)
	}
	if end != nil {
		query += ` AND r.due_at<?`
		args = append(args, *end)
	}
	query += ` ORDER BY r.due_at IS NULL,r.due_at,r.created_at LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]planner.Reminder, 0)
	for rows.Next() {
		item, scanErr := scanReminder(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) CompleteReminder(ctx context.Context, userID, reminderID string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE reminders SET status='completed',updated_at=? WHERE id=UUID_TO_BIN(?) AND user_id=UUID_TO_BIN(?) AND status='active'`, now, reminderID, userID)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return planner.ErrNotFound
	}
	data, _ := json.Marshal(map[string]string{"status": "completed"})
	if _, err = tx.ExecContext(ctx, `INSERT INTO reminder_events (reminder_id,user_id,event_type,event_data,occurred_at) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),'completed',?,?)`, reminderID, userID, data, now); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE notification_deliveries SET status='cancelled',updated_at=? WHERE reminder_id=UUID_TO_BIN(?) AND status='queued'`, now, reminderID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UpdateReminderSync(ctx context.Context, userID, reminderID string, syncResult planner.SyncResult, now time.Time) (planner.Reminder, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return planner.Reminder{}, err
	}
	defer tx.Rollback()
	item, err := scanReminder(tx.QueryRowContext(ctx, reminderSelect+` WHERE r.id=UUID_TO_BIN(?) AND r.user_id=UUID_TO_BIN(?) FOR UPDATE`, reminderID, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return planner.Reminder{}, planner.ErrNotFound
	}
	if err != nil {
		return planner.Reminder{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE reminders SET system_sync_status=?,updated_at=? WHERE id=UUID_TO_BIN(?)`, syncResult.Status, now, reminderID); err != nil {
		return planner.Reminder{}, err
	}
	if syncResult.Provider != "" && syncResult.ExternalID != "" {
		_, err = tx.ExecContext(ctx, `INSERT INTO external_reminder_links (reminder_id,provider,external_id,external_revision,sync_status,last_error_code,created_at,updated_at) VALUES (UUID_TO_BIN(?),?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE external_id=VALUES(external_id),external_revision=VALUES(external_revision),sync_status=VALUES(sync_status),last_error_code=VALUES(last_error_code),updated_at=VALUES(updated_at)`, reminderID, syncResult.Provider, syncResult.ExternalID, syncResult.Revision, syncResult.Status, syncResult.ErrorCode, now, now)
		if err != nil {
			return planner.Reminder{}, err
		}
	}
	data, _ := json.Marshal(syncResult)
	if _, err = tx.ExecContext(ctx, `INSERT INTO reminder_events (reminder_id,user_id,event_type,event_data,occurred_at) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),'sync_updated',?,?)`, reminderID, userID, data, now); err != nil {
		return planner.Reminder{}, err
	}
	if err = tx.Commit(); err != nil {
		return planner.Reminder{}, err
	}
	item.SystemSyncStatus = syncResult.Status
	item.ExternalProvider = syncResult.Provider
	item.ExternalID = syncResult.ExternalID
	item.ExternalRevision = syncResult.Revision
	item.UpdatedAt = now
	return item, nil
}

func (s *Store) EnqueueDueNotifications(ctx context.Context, now time.Time, limit int) (int, error) {
	if limit <= 0 {
		limit = 100
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT BIN_TO_UUID(id),BIN_TO_UUID(reminder_id),BIN_TO_UUID(user_id) FROM notification_deliveries WHERE status='queued' AND enqueued_at IS NULL AND scheduled_at<=? ORDER BY scheduled_at LIMIT ? FOR UPDATE SKIP LOCKED`, now, limit)
	if err != nil {
		return 0, err
	}
	type dueDelivery struct{ id, reminderID, userID string }
	items := make([]dueDelivery, 0)
	for rows.Next() {
		var item dueDelivery
		if err = rows.Scan(&item.id, &item.reminderID, &item.userID); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, item)
	}
	if err = rows.Close(); err != nil {
		return 0, err
	}
	for _, item := range items {
		eventID, idErr := id.New()
		if idErr != nil {
			return 0, idErr
		}
		payload, _ := json.Marshal(map[string]string{"delivery_id": item.id, "reminder_id": item.reminderID, "user_id": item.userID})
		if _, err = tx.ExecContext(ctx, `INSERT INTO outbox_events (id,aggregate_type,aggregate_id,event_type,event_version,payload,occurred_at) VALUES (UUID_TO_BIN(?),'user',UUID_TO_BIN(?),'notification.deliver.v1',1,?,?)`, eventID, item.userID, payload, now); err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE notification_deliveries SET enqueued_at=?,updated_at=? WHERE id=UUID_TO_BIN(?) AND enqueued_at IS NULL`, now, now, item.id); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return len(items), nil
}

func (s *Store) DeliverNotification(ctx context.Context, deliveryID string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE notification_deliveries SET status='delivered',provider_message_id=COALESCE(provider_message_id,CONCAT('in-app:',?)),failure_code=NULL,updated_at=? WHERE id=UUID_TO_BIN(?) AND status='queued' AND enqueued_at IS NOT NULL AND scheduled_at<=?`, deliveryID, now, deliveryID, now)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 1 {
		return nil
	}
	var status string
	if err = s.db.QueryRowContext(ctx, `SELECT status FROM notification_deliveries WHERE id=UUID_TO_BIN(?)`, deliveryID).Scan(&status); errors.Is(err, sql.ErrNoRows) {
		return planner.ErrNotFound
	}
	if err == nil {
		if status == "delivered" || status == "cancelled" || status == "failed" {
			return nil
		}
		return errors.New("notification delivery is not deliverable")
	}
	return err
}

const planSelect = `SELECT BIN_TO_UUID(id),BIN_TO_UUID(user_id),title,DATE_FORMAT(local_date,'%Y-%m-%d'),timezone,status,created_at,updated_at FROM plans`
const planItemSelect = `SELECT BIN_TO_UUID(id),BIN_TO_UUID(plan_id),title,priority,estimated_minutes,starts_at,ends_at,location,status,source,created_at,updated_at FROM plan_items`
const reminderSelect = `SELECT BIN_TO_UUID(r.id),BIN_TO_UUID(r.user_id),COALESCE(BIN_TO_UUID(r.plan_item_id),''),COALESCE(BIN_TO_UUID(r.source_message_id),''),r.raw_text,r.title,r.due_at,r.local_due,r.timezone,COALESCE(r.time_precision,''),r.recurrence,r.needs_clarification,r.status,r.system_sync_status,COALESCE(l.provider,''),COALESCE(l.external_id,''),COALESCE(l.external_revision,''),r.created_at,r.updated_at FROM reminders r LEFT JOIN external_reminder_links l ON l.reminder_id=r.id`

func scanPlan(row rowScanner) (planner.Plan, error) {
	var item planner.Plan
	err := row.Scan(&item.ID, &item.UserID, &item.Title, &item.LocalDate, &item.Timezone, &item.Status, &item.CreatedAt, &item.UpdatedAt)
	return item, err
}
func scanPlanItem(row rowScanner) (planner.PlanItem, error) {
	var item planner.PlanItem
	var starts, ends sql.NullTime
	err := row.Scan(&item.ID, &item.PlanID, &item.Title, &item.Priority, &item.EstimatedMinute, &starts, &ends, &item.Location, &item.Status, &item.Source, &item.CreatedAt, &item.UpdatedAt)
	if starts.Valid {
		item.StartsAt = &starts.Time
	}
	if ends.Valid {
		item.EndsAt = &ends.Time
	}
	return item, err
}
func scanReminder(row rowScanner) (planner.Reminder, error) {
	var item planner.Reminder
	var due sql.NullTime
	var clarification []byte
	err := row.Scan(&item.ID, &item.UserID, &item.PlanItemID, &item.SourceMessageID, &item.RawText, &item.Title, &due, &item.LocalDue, &item.Timezone, &item.TimePrecision, &item.Recurrence, &clarification, &item.Status, &item.SystemSyncStatus, &item.ExternalProvider, &item.ExternalID, &item.ExternalRevision, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return item, err
	}
	if due.Valid {
		item.DueAt = &due.Time
	}
	if len(clarification) > 0 {
		err = json.Unmarshal(clarification, &item.NeedsClarification)
	}
	return item, err
}
