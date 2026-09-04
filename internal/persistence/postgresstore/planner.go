package postgresstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/windcry1/ai-companion/internal/planner"
	"github.com/windcry1/ai-companion/internal/platform/id"
)

var _ planner.Store = (*Store)(nil)

func (s *Store) CreatePlan(ctx context.Context, item planner.Plan) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO app.plans (
			id,user_id,title,local_date,timezone,status,created_at,updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		item.ID, item.UserID, item.Title, item.LocalDate, item.Timezone,
		item.Status, item.CreatedAt, item.UpdatedAt,
	); err != nil {
		return err
	}
	for _, planItem := range item.Items {
		if _, err = tx.ExecContext(ctx, `
			INSERT INTO app.plan_items (
				id,plan_id,title,priority,estimated_minutes,starts_at,ends_at,
				location,status,source,created_at,updated_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			planItem.ID, item.ID, planItem.Title, planItem.Priority,
			planItem.EstimatedMinute, planItem.StartsAt, planItem.EndsAt,
			planItem.Location, planItem.Status, planItem.Source,
			planItem.CreatedAt, planItem.UpdatedAt,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListPlans(ctx context.Context, userID, localDate string) ([]planner.Plan, error) {
	query := planSelect + ` WHERE p.user_id=$1 AND p.status='active'`
	args := []any{userID}
	if localDate != "" {
		args = append(args, localDate)
		query += ` AND p.local_date=$` + placeholder(len(args))
	}
	query += ` ORDER BY p.local_date,p.created_at`
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
		itemRows, itemErr := s.db.QueryContext(ctx, planItemSelect+`
			WHERE pi.plan_id=$1
			ORDER BY pi.starts_at IS NULL,pi.starts_at,pi.created_at`,
			plans[index].ID,
		)
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

func (s *Store) AddPlanItem(ctx context.Context, userID, planID string, item planner.PlanItem) error {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO app.plan_items (
			id,plan_id,title,priority,estimated_minutes,starts_at,ends_at,
			location,status,source,created_at,updated_at
		)
		SELECT
			$1,p.id,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11
		FROM app.plans p
		WHERE p.id=$12 AND p.user_id=$13 AND p.status='active'`,
		item.ID, item.Title, item.Priority, item.EstimatedMinute, item.StartsAt,
		item.EndsAt, item.Location, item.Status, item.Source, item.CreatedAt,
		item.UpdatedAt, planID, userID,
	)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return planner.ErrNotFound
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE app.plans SET updated_at=$1 WHERE id=$2 AND user_id=$3`,
		item.UpdatedAt, planID, userID,
	)
	return err
}

func (s *Store) FindPlanItemBySource(ctx context.Context, userID, localDate, source string) (planner.PlanItem, error) {
	item, err := scanPlanItem(s.db.QueryRowContext(ctx, planItemSelect+`
		JOIN app.plans p ON p.id=pi.plan_id
		WHERE p.user_id=$1
			AND p.local_date=$2
			AND p.status='active'
			AND pi.source=$3
		ORDER BY pi.created_at
		LIMIT 1`,
		userID, localDate, source,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return planner.PlanItem{}, planner.ErrNotFound
	}
	return item, err
}

func (s *Store) GetPlanItem(ctx context.Context, userID, itemID string) (planner.PlanItem, planner.Plan, error) {
	item, err := scanPlanItem(s.db.QueryRowContext(ctx, planItemSelect+`
		JOIN app.plans p ON p.id=pi.plan_id
		WHERE pi.id=$1 AND p.user_id=$2 AND p.status='active'`,
		itemID, userID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return planner.PlanItem{}, planner.Plan{}, planner.ErrNotFound
	}
	if err != nil {
		return planner.PlanItem{}, planner.Plan{}, err
	}
	plan, err := scanPlan(s.db.QueryRowContext(
		ctx, planSelect+` WHERE p.id=$1 AND p.user_id=$2 AND p.status='active'`,
		item.PlanID, userID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return planner.PlanItem{}, planner.Plan{}, planner.ErrNotFound
	}
	return item, plan, err
}

func (s *Store) UpdatePlanItemSchedule(ctx context.Context, userID string, updated planner.PlanItem, expectedUpdatedAt *time.Time, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	query := `
		UPDATE app.plan_items pi SET
			starts_at=$1,ends_at=$2,updated_at=$3
		FROM app.plans p
		WHERE p.id=pi.plan_id
			AND pi.id=$4
			AND p.user_id=$5
			AND p.status='active'
			AND pi.status IN ('pending','in_progress')`
	args := []any{updated.StartsAt, updated.EndsAt, now, updated.ID, userID}
	if expectedUpdatedAt != nil {
		args = append(args, *expectedUpdatedAt)
		query += ` AND pi.updated_at=$` + placeholder(len(args))
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 1 {
		if _, err = tx.ExecContext(ctx, `
			UPDATE app.plans SET updated_at=$1 WHERE id=$2`,
			now, updated.PlanID,
		); err != nil {
			return err
		}
		return tx.Commit()
	}
	_ = tx.Rollback()
	current, _, err := s.GetPlanItem(ctx, userID, updated.ID)
	if err != nil {
		return err
	}
	if sameNullableTime(current.StartsAt, updated.StartsAt) {
		return nil
	}
	return planner.ErrStaleUpdate
}

func (s *Store) CompletePlanItem(ctx context.Context, userID, itemID string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var planID string
	result, err := tx.ExecContext(ctx, `
		UPDATE app.plan_items pi SET status='completed',updated_at=$1
		FROM app.plans p
		WHERE p.id=pi.plan_id
			AND pi.id=$2
			AND p.user_id=$3
			AND p.status='active'
			AND pi.status IN ('pending','in_progress')`,
		now, itemID, userID,
	)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		var status string
		err = tx.QueryRowContext(ctx, `
			SELECT pi.status
			FROM app.plan_items pi
			JOIN app.plans p ON p.id=pi.plan_id
			WHERE pi.id=$1 AND p.user_id=$2 AND p.status='active'`,
			itemID, userID,
		).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) || err == nil && status != "completed" {
			return planner.ErrNotFound
		}
		if err != nil {
			return err
		}
	}
	if err = tx.QueryRowContext(ctx, `
		SELECT pi.plan_id::text
		FROM app.plan_items pi
		JOIN app.plans p ON p.id=pi.plan_id
		WHERE pi.id=$1 AND p.user_id=$2`,
		itemID, userID,
	).Scan(&planID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE app.plans SET updated_at=$1 WHERE id=$2`,
		now, planID,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateReminder(ctx context.Context, item planner.Reminder) error {
	clarifications, err := json.Marshal(item.NeedsClarification)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO app.reminders (
			id,user_id,plan_item_id,source_message_id,raw_text,title,due_at,
			local_due,timezone,time_precision,recurrence,needs_clarification,
			status,system_sync_status,created_at,updated_at
		) VALUES (
			$1,$2,NULLIF($3,'')::uuid,NULLIF($4,'')::uuid,$5,$6,$7,$8,$9,
			NULLIF($10,''),$11,$12,$13,$14,$15,$16
		)`,
		item.ID, item.UserID, item.PlanItemID, item.SourceMessageID, item.RawText,
		item.Title, item.DueAt, item.LocalDue, item.Timezone, item.TimePrecision,
		item.Recurrence, clarifications, item.Status, item.SystemSyncStatus,
		item.CreatedAt, item.UpdatedAt,
	)
	return err
}

func (s *Store) GetReminder(ctx context.Context, userID, reminderID string) (planner.Reminder, error) {
	item, err := scanReminder(s.db.QueryRowContext(ctx, reminderSelect+`
		WHERE r.id=$1 AND r.user_id=$2 AND r.status<>'cancelled'`,
		reminderID, userID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		err = planner.ErrNotFound
	}
	return item, err
}

func (s *Store) FindReminderBySourceMessage(ctx context.Context, userID, sourceMessageID string) (planner.Reminder, error) {
	item, err := scanReminder(s.db.QueryRowContext(ctx, reminderSelect+`
		WHERE r.user_id=$1
			AND r.source_message_id=$2
			AND r.status<>'cancelled'
		ORDER BY r.created_at
		LIMIT 1`,
		userID, sourceMessageID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return planner.Reminder{}, planner.ErrNotFound
	}
	return item, err
}

func (s *Store) ConfirmReminder(ctx context.Context, item planner.Reminder, key string, now time.Time) (planner.Reminder, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return planner.Reminder{}, false, err
	}
	defer tx.Rollback()
	locked, err := scanReminder(tx.QueryRowContext(ctx, reminderSelect+`
		WHERE r.id=$1 AND r.user_id=$2
		FOR UPDATE OF r`,
		item.ID, item.UserID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return planner.Reminder{}, false, planner.ErrNotFound
	}
	if err != nil {
		return planner.Reminder{}, false, err
	}
	var owner string
	err = tx.QueryRowContext(ctx, `
		SELECT id::text
		FROM app.reminders
		WHERE user_id=$1 AND confirmation_key=$2
		LIMIT 1`,
		item.UserID, key,
	).Scan(&owner)
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
	if _, err = tx.ExecContext(ctx, `
		UPDATE app.reminders
		SET status='active',confirmation_key=$1,updated_at=$2
		WHERE id=$3`,
		key, now, item.ID,
	); err != nil {
		return planner.Reminder{}, false, err
	}
	eventData, _ := json.Marshal(map[string]any{
		"due_at":   locked.DueAt,
		"timezone": locked.Timezone,
	})
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO app.reminder_events (
			reminder_id,user_id,event_type,event_data,occurred_at
		) VALUES ($1,$2,'confirmed',$3,$4)`,
		item.ID, item.UserID, eventData, now,
	); err != nil {
		return planner.Reminder{}, false, err
	}
	if locked.TimePrecision == "minute" {
		deliveryID, deliveryErr := id.New()
		if deliveryErr != nil {
			return planner.Reminder{}, false, deliveryErr
		}
		if _, err = tx.ExecContext(ctx, `
			INSERT INTO app.notification_deliveries (
				id,reminder_id,user_id,channel,scheduled_at,status,created_at,updated_at
			) VALUES ($1,$2,$3,'in_app',$4,'queued',$5,$5)`,
			deliveryID, item.ID, item.UserID, *locked.DueAt, now,
		); err != nil {
			return planner.Reminder{}, false, err
		}
	}
	metadata, _ := json.Marshal(map[string]string{
		"timezone":  locked.Timezone,
		"local_due": locked.LocalDue,
	})
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO eventing.audit_logs (
			actor_type,actor_id,action,resource_type,resource_id,metadata,occurred_at
		) VALUES ('user',$1,'reminder.confirm','reminder',$2,$3,$4)`,
		item.UserID, item.ID, metadata, now,
	); err != nil {
		return planner.Reminder{}, false, err
	}
	eventID, err := id.New()
	if err != nil {
		return planner.Reminder{}, false, err
	}
	payload, _ := json.Marshal(map[string]string{
		"reminder_id": item.ID,
		"user_id":     item.UserID,
	})
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO eventing.outbox_events (
			id,aggregate_type,aggregate_id,event_type,event_version,payload,occurred_at
		) VALUES ($1,'reminder',$2,'reminder.confirmed.v1',1,$3,$4)`,
		eventID, item.ID, payload, now,
	); err != nil {
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
	query := reminderSelect + ` WHERE r.user_id=$1 AND r.status<>'cancelled'`
	args := []any{userID}
	if start != nil {
		args = append(args, *start)
		query += ` AND r.due_at>=$` + placeholder(len(args))
	}
	if end != nil {
		args = append(args, *end)
		query += ` AND r.due_at<$` + placeholder(len(args))
	}
	args = append(args, limit)
	query += ` ORDER BY r.due_at IS NULL,r.due_at,r.created_at LIMIT $` + placeholder(len(args))
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

func (s *Store) ListTodayReminders(ctx context.Context, userID string, start, end time.Time, limit int) ([]planner.Reminder, error) {
	rows, err := s.db.QueryContext(ctx, reminderSelect+`
		WHERE r.user_id=$1
			AND r.due_at<$2
			AND (
				r.status='active'
				OR (r.status='completed' AND r.due_at>=$3)
			)
		ORDER BY r.due_at,r.created_at
		LIMIT $4`,
		userID, end, start, limit,
	)
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
	result, err := tx.ExecContext(ctx, `
		UPDATE app.reminders
		SET status='completed',updated_at=$1
		WHERE id=$2 AND user_id=$3 AND status='active'`,
		now, reminderID, userID,
	)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return planner.ErrNotFound
	}
	eventData, _ := json.Marshal(map[string]string{"status": "completed"})
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO app.reminder_events (
			reminder_id,user_id,event_type,event_data,occurred_at
		) VALUES ($1,$2,'completed',$3,$4)`,
		reminderID, userID, eventData, now,
	); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE app.notification_deliveries
		SET status='cancelled',updated_at=$1
		WHERE reminder_id=$2 AND status='queued'`,
		now, reminderID,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RescheduleReminder(ctx context.Context, updated planner.Reminder, expectedUpdatedAt *time.Time, now time.Time) (planner.Reminder, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return planner.Reminder{}, err
	}
	defer tx.Rollback()
	current, err := scanReminder(tx.QueryRowContext(ctx, reminderSelect+`
		WHERE r.id=$1 AND r.user_id=$2
		FOR UPDATE OF r`,
		updated.ID, updated.UserID,
	))
	if errors.Is(err, sql.ErrNoRows) || err == nil && current.Status != "active" {
		return planner.Reminder{}, planner.ErrNotFound
	}
	if err != nil {
		return planner.Reminder{}, err
	}
	if expectedUpdatedAt != nil && !current.UpdatedAt.Equal(*expectedUpdatedAt) {
		if current.DueAt != nil && updated.DueAt != nil &&
			current.DueAt.Equal(*updated.DueAt) &&
			current.TimePrecision == updated.TimePrecision {
			return current, nil
		}
		return planner.Reminder{}, planner.ErrStaleUpdate
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE app.reminders SET
			due_at=$1,local_due=$2,timezone=$3,time_precision=$4,
			system_sync_status=$5,updated_at=$6
		WHERE id=$7`,
		updated.DueAt, updated.LocalDue, updated.Timezone, updated.TimePrecision,
		updated.SystemSyncStatus, now, updated.ID,
	); err != nil {
		return planner.Reminder{}, err
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE app.notification_deliveries
		SET status='cancelled',updated_at=$1
		WHERE reminder_id=$2 AND status='queued'`,
		now, updated.ID,
	); err != nil {
		return planner.Reminder{}, err
	}
	if updated.TimePrecision == "minute" && updated.DueAt != nil {
		deliveryID, idErr := id.New()
		if idErr != nil {
			return planner.Reminder{}, idErr
		}
		if _, err = tx.ExecContext(ctx, `
			INSERT INTO app.notification_deliveries (
				id,reminder_id,user_id,channel,scheduled_at,status,enqueued_at,
				provider_message_id,failure_code,created_at,updated_at
			) VALUES ($1,$2,$3,'in_app',$4,'queued',NULL,NULL,NULL,$5,$5)
			ON CONFLICT (reminder_id,channel,scheduled_at) DO UPDATE SET
				status='queued',
				enqueued_at=NULL,
				provider_message_id=NULL,
				failure_code=NULL,
				updated_at=EXCLUDED.updated_at`,
			deliveryID, updated.ID, updated.UserID, *updated.DueAt, now,
		); err != nil {
			return planner.Reminder{}, err
		}
	}
	eventData, _ := json.Marshal(map[string]any{
		"old_local_due":      current.LocalDue,
		"new_local_due":      updated.LocalDue,
		"old_time_precision": current.TimePrecision,
		"new_time_precision": updated.TimePrecision,
	})
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO app.reminder_events (
			reminder_id,user_id,event_type,event_data,occurred_at
		) VALUES ($1,$2,'rescheduled',$3,$4)`,
		updated.ID, updated.UserID, eventData, now,
	); err != nil {
		return planner.Reminder{}, err
	}
	eventID, idErr := id.New()
	if idErr != nil {
		return planner.Reminder{}, idErr
	}
	payload, _ := json.Marshal(map[string]string{
		"reminder_id": updated.ID,
		"user_id":     updated.UserID,
		"local_due":   updated.LocalDue,
	})
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO eventing.outbox_events (
			id,aggregate_type,aggregate_id,event_type,event_version,payload,occurred_at
		) VALUES ($1,'reminder',$2,'reminder.rescheduled.v1',1,$3,$4)`,
		eventID, updated.ID, payload, now,
	); err != nil {
		return planner.Reminder{}, err
	}
	if err = tx.Commit(); err != nil {
		return planner.Reminder{}, err
	}
	updated.UpdatedAt = now
	return updated, nil
}

func (s *Store) UpdateReminderSync(ctx context.Context, userID, reminderID string, syncResult planner.SyncResult, now time.Time) (planner.Reminder, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return planner.Reminder{}, err
	}
	defer tx.Rollback()
	item, err := scanReminder(tx.QueryRowContext(ctx, reminderSelect+`
		WHERE r.id=$1 AND r.user_id=$2
		FOR UPDATE OF r`,
		reminderID, userID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return planner.Reminder{}, planner.ErrNotFound
	}
	if err != nil {
		return planner.Reminder{}, err
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE app.reminders SET system_sync_status=$1,updated_at=$2 WHERE id=$3`,
		syncResult.Status, now, reminderID,
	); err != nil {
		return planner.Reminder{}, err
	}
	if syncResult.Provider != "" && syncResult.ExternalID != "" {
		if _, err = tx.ExecContext(ctx, `
			INSERT INTO app.external_reminder_links (
				reminder_id,provider,external_id,external_revision,sync_status,
				last_error_code,created_at,updated_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$7)
			ON CONFLICT (reminder_id) DO UPDATE SET
				provider=EXCLUDED.provider,
				external_id=EXCLUDED.external_id,
				external_revision=EXCLUDED.external_revision,
				sync_status=EXCLUDED.sync_status,
				last_error_code=EXCLUDED.last_error_code,
				updated_at=EXCLUDED.updated_at`,
			reminderID, syncResult.Provider, syncResult.ExternalID,
			syncResult.Revision, syncResult.Status, syncResult.ErrorCode, now,
		); err != nil {
			return planner.Reminder{}, err
		}
	}
	eventData, _ := json.Marshal(syncResult)
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO app.reminder_events (
			reminder_id,user_id,event_type,event_data,occurred_at
		) VALUES ($1,$2,'sync_updated',$3,$4)`,
		reminderID, userID, eventData, now,
	); err != nil {
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
	rows, err := tx.QueryContext(ctx, `
		SELECT id::text,reminder_id::text,user_id::text
		FROM app.notification_deliveries
		WHERE status='queued'
			AND enqueued_at IS NULL
			AND scheduled_at<=$1
		ORDER BY scheduled_at
		LIMIT $2
		FOR UPDATE SKIP LOCKED`,
		now, limit,
	)
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
		payload, _ := json.Marshal(map[string]string{
			"delivery_id": item.id,
			"reminder_id": item.reminderID,
			"user_id":     item.userID,
		})
		if _, err = tx.ExecContext(ctx, `
			INSERT INTO eventing.outbox_events (
				id,aggregate_type,aggregate_id,event_type,event_version,payload,occurred_at
			) VALUES ($1,'user',$2,'notification.deliver.v1',1,$3,$4)`,
			eventID, item.userID, payload, now,
		); err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, `
			UPDATE app.notification_deliveries
			SET enqueued_at=$1,updated_at=$1
			WHERE id=$2 AND enqueued_at IS NULL`,
			now, item.id,
		); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return len(items), nil
}

func (s *Store) DeliverNotification(ctx context.Context, deliveryID string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE app.notification_deliveries SET
			status='delivered',
			provider_message_id=COALESCE(provider_message_id,'in-app:' || $1),
			failure_code=NULL,
			updated_at=$2
		WHERE id=$1
			AND status='queued'
			AND enqueued_at IS NOT NULL
			AND scheduled_at<=$2`,
		deliveryID, now,
	)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 1 {
		return nil
	}
	var status string
	err = s.db.QueryRowContext(ctx, `
		SELECT status FROM app.notification_deliveries WHERE id=$1`,
		deliveryID,
	).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
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

func (s *Store) DeliverNextNotification(ctx context.Context, now time.Time) (bool, error) {
	var deliveryID string
	err := s.db.QueryRowContext(ctx, `
		WITH candidate AS (
			SELECT id
			FROM app.notification_deliveries
			WHERE status='queued'
				AND enqueued_at IS NOT NULL
				AND scheduled_at<=$1
			ORDER BY scheduled_at,id
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE app.notification_deliveries AS delivery SET
			status='delivered',
			provider_message_id=COALESCE(delivery.provider_message_id,'in-app:' || delivery.id::text),
			failure_code=NULL,
			updated_at=$1
		FROM candidate
		WHERE delivery.id=candidate.id
		RETURNING delivery.id::text`,
		now,
	).Scan(&deliveryID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

const planSelect = `
	SELECT
		p.id::text,p.user_id::text,p.title,TO_CHAR(p.local_date,'YYYY-MM-DD'),
		p.timezone,p.status,p.created_at,p.updated_at
	FROM app.plans p`

const planItemSelect = `
	SELECT
		pi.id::text,pi.plan_id::text,pi.title,pi.priority,pi.estimated_minutes,
		pi.starts_at,pi.ends_at,pi.location,pi.status,pi.source,
		pi.created_at,pi.updated_at
	FROM app.plan_items pi`

const reminderSelect = `
	SELECT
		r.id::text,r.user_id::text,COALESCE(r.plan_item_id::text,''),
		COALESCE(r.source_message_id::text,''),r.raw_text,r.title,r.due_at,
		r.local_due,r.timezone,COALESCE(r.time_precision,''),r.recurrence,
		r.needs_clarification,r.status,r.system_sync_status,
		COALESCE(l.provider,''),COALESCE(l.external_id,''),
		COALESCE(l.external_revision,''),r.created_at,r.updated_at
	FROM app.reminders r
	LEFT JOIN app.external_reminder_links l ON l.reminder_id=r.id`

func scanPlan(row rowScanner) (planner.Plan, error) {
	var item planner.Plan
	err := row.Scan(
		&item.ID, &item.UserID, &item.Title, &item.LocalDate,
		&item.Timezone, &item.Status, &item.CreatedAt, &item.UpdatedAt,
	)
	return item, err
}

func scanPlanItem(row rowScanner) (planner.PlanItem, error) {
	var item planner.PlanItem
	var starts, ends sql.NullTime
	err := row.Scan(
		&item.ID, &item.PlanID, &item.Title, &item.Priority,
		&item.EstimatedMinute, &starts, &ends, &item.Location, &item.Status,
		&item.Source, &item.CreatedAt, &item.UpdatedAt,
	)
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
	var clarifications []byte
	err := row.Scan(
		&item.ID, &item.UserID, &item.PlanItemID, &item.SourceMessageID,
		&item.RawText, &item.Title, &due, &item.LocalDue, &item.Timezone,
		&item.TimePrecision, &item.Recurrence, &clarifications, &item.Status,
		&item.SystemSyncStatus, &item.ExternalProvider, &item.ExternalID,
		&item.ExternalRevision, &item.CreatedAt, &item.UpdatedAt,
	)
	if err != nil {
		return item, err
	}
	if due.Valid {
		item.DueAt = &due.Time
	}
	if len(clarifications) > 0 {
		err = json.Unmarshal(clarifications, &item.NeedsClarification)
	}
	return item, err
}

func sameNullableTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}
