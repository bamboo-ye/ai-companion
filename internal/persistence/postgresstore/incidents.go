package postgresstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/incident"
	"github.com/windcry1/ai-companion/internal/platform/id"
)

var _ incident.Store = (*Store)(nil)

func (s *Store) ListAlertRules(ctx context.Context) ([]incident.AlertRule, error) {
	rows, err := s.db.QueryContext(ctx, alertRuleSelect+` ORDER BY enabled DESC,CASE severity WHEN 'critical' THEN 0 ELSE 1 END,name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []incident.AlertRule{}
	for rows.Next() {
		item, scanErr := scanAlertRule(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) CreateAlertRule(ctx context.Context, rule incident.AlertRule, reason string) (incident.AlertRule, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return incident.AlertRule{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO ops.alert_rules
			(id,name,description,service,level,event_prefix,window_minutes,threshold,severity,enabled,created_by,updated_by,created_at,updated_at)
		VALUES ($1,$2,$3,NULLIF($4,''),$5,NULLIF($6,''),$7,$8,$9,$10,$11,$11,$12,$12)`,
		rule.ID, rule.Name, rule.Description, rule.Service, rule.Level, rule.EventPrefix, rule.WindowMinutes, rule.Threshold, rule.Severity, rule.Enabled, rule.CreatedBy, rule.CreatedAt)
	if err != nil {
		return incident.AlertRule{}, err
	}
	if err = insertIncidentAudit(ctx, tx, "operator", rule.CreatedBy, "alert_rule.create", "alert_rule", rule.ID, reason, rule.CreatedAt, map[string]any{"name": rule.Name}); err != nil {
		return incident.AlertRule{}, err
	}
	if err = tx.Commit(); err != nil {
		return incident.AlertRule{}, err
	}
	return rule, nil
}

func (s *Store) SetAlertRuleEnabled(ctx context.Context, ruleID string, enabled bool, actor, reason string, now time.Time) (incident.AlertRule, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return incident.AlertRule{}, err
	}
	defer tx.Rollback()
	row := tx.QueryRowContext(ctx, alertRuleSelect+` WHERE id=$1 FOR UPDATE`, ruleID)
	rule, err := scanAlertRule(row)
	if errors.Is(err, sql.ErrNoRows) {
		return incident.AlertRule{}, incident.ErrNotFound
	}
	if err != nil {
		return incident.AlertRule{}, err
	}
	var activeIncident *incident.Incident
	if !enabled {
		active, activeErr := scanIncident(tx.QueryRowContext(ctx, incidentSelect+` WHERE i.rule_id=$1 AND i.status IN ('open','acknowledged') FOR UPDATE OF i`, ruleID))
		if activeErr != nil && !errors.Is(activeErr, sql.ErrNoRows) {
			return incident.AlertRule{}, activeErr
		}
		if activeErr == nil {
			activeIncident = &active
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE ops.alert_rules SET enabled=$1,updated_by=$2,updated_at=$3 WHERE id=$4`, enabled, actor, now, ruleID)
	if err != nil {
		return incident.AlertRule{}, err
	}
	if !enabled {
		_, err = tx.ExecContext(ctx, `UPDATE ops.incidents SET status='resolved',resolved_at=$1,resolved_by=$2,resolution=$3,updated_at=$1 WHERE rule_id=$4 AND status IN ('open','acknowledged')`, now, actor, "rule disabled: "+reason, ruleID)
		if err != nil {
			return incident.AlertRule{}, err
		}
		if activeIncident != nil {
			activeIncident.Status, activeIncident.ResolvedAt, activeIncident.ResolvedBy, activeIncident.Resolution, activeIncident.UpdatedAt = "resolved", &now, actor, "rule disabled: "+reason, now
			if err = insertIncidentAudit(ctx, tx, "operator", actor, "incident.resolved", "incident", activeIncident.ID, activeIncident.Resolution, now, map[string]any{"rule_id": ruleID, "source": "rule_disabled"}); err != nil {
				return incident.AlertRule{}, err
			}
			if _, err = queueIncidentNotificationsTx(ctx, tx, *activeIncident, "resolved", now); err != nil {
				return incident.AlertRule{}, err
			}
		}
	}
	if err = insertIncidentAudit(ctx, tx, "operator", actor, "alert_rule.enabled.update", "alert_rule", ruleID, reason, now, map[string]any{"enabled": enabled}); err != nil {
		return incident.AlertRule{}, err
	}
	if err = tx.Commit(); err != nil {
		return incident.AlertRule{}, err
	}
	rule.Enabled, rule.UpdatedBy, rule.UpdatedAt = enabled, actor, now
	return rule, nil
}

func (s *Store) CountAlertMatches(ctx context.Context, rule incident.AlertRule, since, until time.Time) (int, error) {
	query := `SELECT COUNT(*) FROM ops.system_logs WHERE occurred_at >= $1 AND occurred_at <= $2 AND level=$3`
	args := []any{since, until, rule.Level}
	if rule.Service != "" {
		args = append(args, rule.Service)
		query += ` AND service=$` + placeholder(len(args))
	}
	if rule.EventPrefix != "" {
		args = append(args, rule.EventPrefix+"%")
		query += ` AND event LIKE $` + placeholder(len(args))
	}
	var count int
	return count, s.db.QueryRowContext(ctx, query, args...).Scan(&count)
}

func (s *Store) ReconcileAlertIncident(ctx context.Context, rule incident.AlertRule, count int, now time.Time) (*incident.Incident, string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	current, err := scanIncident(tx.QueryRowContext(ctx, incidentSelect+` WHERE i.rule_id=$1 AND i.status IN ('open','acknowledged') FOR UPDATE OF i`, rule.ID))
	found := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, "", err
	}
	windowStart := now.Add(-time.Duration(rule.WindowMinutes) * time.Minute)
	evidence, _ := json.Marshal(map[string]any{"source": "ops.system_logs", "service": rule.Service, "level": rule.Level, "event_prefix": rule.EventPrefix, "window_started_at": windowStart, "window_ended_at": now})
	if count >= rule.Threshold {
		if found {
			current.ObservedValue, current.WindowStartedAt, current.WindowEndedAt, current.Evidence, current.UpdatedAt = count, windowStart, now, evidence, now
			current.Summary = fmt.Sprintf("%d 条日志达到阈值 %d", count, rule.Threshold)
			_, err = tx.ExecContext(ctx, `UPDATE ops.incidents SET observed_value=$1,summary=$2,window_started_at=$3,window_ended_at=$4,evidence=$5,updated_at=$4 WHERE id=$6`, count, current.Summary, windowStart, now, evidence, current.ID)
			if err != nil {
				return nil, "", err
			}
			if err = tx.Commit(); err != nil {
				return nil, "", err
			}
			return &current, "updated", nil
		}
		incidentID, idErr := id.New()
		if idErr != nil {
			return nil, "", idErr
		}
		current = incident.Incident{ID: incidentID, RuleID: rule.ID, RuleName: rule.Name, Status: "open", Severity: rule.Severity, Title: rule.Name, Summary: fmt.Sprintf("%d 条日志达到阈值 %d", count, rule.Threshold), Service: rule.Service, Level: rule.Level, EventPrefix: rule.EventPrefix, ObservedValue: count, Threshold: rule.Threshold, WindowMinutes: rule.WindowMinutes, WindowStartedAt: windowStart, WindowEndedAt: now, Evidence: evidence, OpenedAt: now, UpdatedAt: now}
		_, err = tx.ExecContext(ctx, `INSERT INTO ops.incidents (id,rule_id,source_type,source_id,status,severity,title,summary,service,level,event_prefix,observed_value,threshold,window_minutes,window_started_at,window_ended_at,evidence,opened_at,updated_at) VALUES ($1,$2,'log_rule',$2,'open',$3,$4,$5,NULLIF($6,''),$7,NULLIF($8,''),$9,$10,$11,$12,$13,$14,$13,$13)`, current.ID, rule.ID, current.Severity, current.Title, current.Summary, current.Service, current.Level, current.EventPrefix, count, rule.Threshold, rule.WindowMinutes, windowStart, now, evidence)
		if err != nil {
			return nil, "", err
		}
		if err = insertIncidentAudit(ctx, tx, "system", "alert-evaluator", "incident.opened", "incident", current.ID, "threshold reached", now, map[string]any{"rule_id": rule.ID, "observed_value": count, "threshold": rule.Threshold}); err != nil {
			return nil, "", err
		}
		if _, err = queueIncidentNotificationsTx(ctx, tx, current, "opened", now); err != nil {
			return nil, "", err
		}
		if err = tx.Commit(); err != nil {
			return nil, "", err
		}
		return &current, "opened", nil
	}
	if !found {
		_ = tx.Rollback()
		return nil, "steady", nil
	}
	resolution := "signal returned below threshold"
	_, err = tx.ExecContext(ctx, `UPDATE ops.incidents SET status='resolved',observed_value=$1,window_started_at=$2,window_ended_at=$3,evidence=$4,resolved_at=$3,resolved_by='system',resolution=$5,updated_at=$3 WHERE id=$6`, count, windowStart, now, evidence, resolution, current.ID)
	if err != nil {
		return nil, "", err
	}
	if err = insertIncidentAudit(ctx, tx, "system", "alert-evaluator", "incident.auto_resolved", "incident", current.ID, resolution, now, map[string]any{"rule_id": rule.ID, "observed_value": count}); err != nil {
		return nil, "", err
	}
	current.Status, current.ObservedValue, current.ResolvedAt, current.ResolvedBy, current.Resolution, current.UpdatedAt = "resolved", count, &now, "system", resolution, now
	if _, err = queueIncidentNotificationsTx(ctx, tx, current, "resolved", now); err != nil {
		return nil, "", err
	}
	if err = tx.Commit(); err != nil {
		return nil, "", err
	}
	return &current, "resolved", nil
}

func (s *Store) ListIncidents(ctx context.Context, filter incident.IncidentFilter) ([]incident.Incident, error) {
	query := incidentSelect + ` WHERE TRUE`
	args := []any{}
	if filter.Status != "" {
		args = append(args, filter.Status)
		query += ` AND i.status=$` + placeholder(len(args))
	}
	if filter.Severity != "" {
		args = append(args, filter.Severity)
		query += ` AND i.severity=$` + placeholder(len(args))
	}
	if filter.Source != "" {
		args = append(args, filter.Source)
		query += ` AND i.source_type=$` + placeholder(len(args))
	}
	args = append(args, filter.Limit)
	query += ` ORDER BY CASE i.status WHEN 'open' THEN 0 WHEN 'acknowledged' THEN 1 ELSE 2 END,i.opened_at DESC LIMIT $` + placeholder(len(args))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []incident.Incident{}
	for rows.Next() {
		item, scanErr := scanIncident(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetIncident(ctx context.Context, incidentID string) (incident.Incident, error) {
	item, err := scanIncident(s.db.QueryRowContext(ctx, incidentSelect+` WHERE i.id=$1`, incidentID))
	if errors.Is(err, sql.ErrNoRows) {
		return incident.Incident{}, incident.ErrNotFound
	}
	return item, err
}

func (s *Store) SetIncidentStatus(ctx context.Context, incidentID, status, actor, reason string, now time.Time) (incident.Incident, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return incident.Incident{}, err
	}
	defer tx.Rollback()
	item, err := scanIncident(tx.QueryRowContext(ctx, incidentSelect+` WHERE i.id=$1 FOR UPDATE OF i`, incidentID))
	if errors.Is(err, sql.ErrNoRows) {
		return incident.Incident{}, incident.ErrNotFound
	}
	if err != nil {
		return incident.Incident{}, err
	}
	if item.Status == "resolved" || (status == "acknowledged" && item.Status != "open") {
		return incident.Incident{}, incident.ErrConflict
	}
	if status == "acknowledged" {
		_, err = tx.ExecContext(ctx, `UPDATE ops.incidents SET status='acknowledged',acknowledged_at=$1,acknowledged_by=$2,updated_at=$1 WHERE id=$3`, now, actor, incidentID)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE ops.incidents SET status='resolved',resolved_at=$1,resolved_by=$2,resolution=$3,updated_at=$1 WHERE id=$4`, now, actor, reason, incidentID)
	}
	if err != nil {
		return incident.Incident{}, err
	}
	if err = insertIncidentAudit(ctx, tx, "operator", actor, "incident."+status, "incident", incidentID, reason, now, nil); err != nil {
		return incident.Incident{}, err
	}
	if status == "resolved" {
		item.Status, item.ResolvedAt, item.ResolvedBy, item.Resolution, item.UpdatedAt = "resolved", &now, actor, reason, now
		if _, err = queueIncidentNotificationsTx(ctx, tx, item, "resolved", now); err != nil {
			return incident.Incident{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return incident.Incident{}, err
	}
	return s.GetIncident(ctx, incidentID)
}

func (s *Store) ListAlertSubscriptions(ctx context.Context) ([]incident.AlertSubscription, error) {
	rows, err := s.db.QueryContext(ctx, alertSubscriptionSelect+` ORDER BY enabled DESC,name,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []incident.AlertSubscription{}
	for rows.Next() {
		item, scanErr := scanAlertSubscription(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) CreateAlertSubscription(ctx context.Context, item incident.AlertSubscription, reason string) (incident.AlertSubscription, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return incident.AlertSubscription{}, err
	}
	defer tx.Rollback()
	if item.RuleID != "" {
		var exists bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ops.alert_rules WHERE id=$1)`, item.RuleID).Scan(&exists); err != nil {
			return incident.AlertSubscription{}, err
		}
		if !exists {
			return incident.AlertSubscription{}, incident.ErrNotFound
		}
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO ops.alert_subscriptions
			(id,name,rule_id,channel,target,minimum_severity,notify_on_open,notify_on_resolved,enabled,created_by,updated_by,created_at,updated_at)
		VALUES ($1,$2,NULLIF($3,'')::uuid,$4,$5,$6,$7,$8,$9,$10,$10,$11,$11)`,
		item.ID, item.Name, item.RuleID, item.Channel, item.Target, item.MinimumSeverity, item.NotifyOnOpen, item.NotifyOnResolved, item.Enabled, item.CreatedBy, item.CreatedAt)
	if isUniqueViolation(err) {
		return incident.AlertSubscription{}, incident.ErrConflict
	}
	if err != nil {
		return incident.AlertSubscription{}, err
	}
	if err = insertIncidentAudit(ctx, tx, "operator", item.CreatedBy, "alert_subscription.create", "alert_subscription", item.ID, reason, item.CreatedAt, map[string]any{"channel": item.Channel, "minimum_severity": item.MinimumSeverity, "rule_id": item.RuleID}); err != nil {
		return incident.AlertSubscription{}, err
	}
	if err = tx.Commit(); err != nil {
		return incident.AlertSubscription{}, err
	}
	return item, nil
}

func (s *Store) SetAlertSubscriptionEnabled(ctx context.Context, subscriptionID string, enabled bool, actor, reason string, now time.Time) (incident.AlertSubscription, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return incident.AlertSubscription{}, err
	}
	defer tx.Rollback()
	item, err := scanAlertSubscription(tx.QueryRowContext(ctx, alertSubscriptionSelect+` WHERE s.id=$1 FOR UPDATE OF s`, subscriptionID))
	if errors.Is(err, sql.ErrNoRows) {
		return incident.AlertSubscription{}, incident.ErrNotFound
	}
	if err != nil {
		return incident.AlertSubscription{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE ops.alert_subscriptions SET enabled=$1,updated_by=$2,updated_at=$3 WHERE id=$4`, enabled, actor, now, subscriptionID); err != nil {
		return incident.AlertSubscription{}, err
	}
	if err = insertIncidentAudit(ctx, tx, "operator", actor, "alert_subscription.enabled.update", "alert_subscription", subscriptionID, reason, now, map[string]any{"enabled": enabled}); err != nil {
		return incident.AlertSubscription{}, err
	}
	if err = tx.Commit(); err != nil {
		return incident.AlertSubscription{}, err
	}
	item.Enabled, item.UpdatedBy, item.UpdatedAt = enabled, actor, now
	return item, nil
}

func (s *Store) ListIncidentNotifications(ctx context.Context, incidentID string) ([]incident.IncidentNotification, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT n.id::text,n.incident_id::text,n.transition,n.channel,n.target,COALESCE(n.delivery_id::text,n.id::text),
			CASE WHEN n.channel='email' THEN d.status ELSE n.status END,
			CASE WHEN n.channel='email' THEN d.attempts ELSE n.attempts END,
			CASE WHEN n.channel='email' THEN d.failure_code ELSE n.failure_code END,
			n.created_at,CASE WHEN n.channel='email' THEN d.sent_at ELSE n.sent_at END
		FROM ops.incident_notifications n
		LEFT JOIN app.email_deliveries d ON d.id=n.delivery_id
		WHERE n.incident_id=$1
		ORDER BY n.created_at DESC,n.id DESC`, incidentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []incident.IncidentNotification{}
	for rows.Next() {
		var item incident.IncidentNotification
		var target string
		if err = rows.Scan(&item.ID, &item.IncidentID, &item.Transition, &item.Channel, &target, &item.DeliveryID, &item.Status, &item.Attempts, &item.FailureCode, &item.CreatedAt, &item.SentAt); err != nil {
			return nil, err
		}
		if item.Channel == "email" {
			item.Target = maskIncidentEmail(target)
		} else {
			item.Target = target
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ListIncidentActivities(ctx context.Context, incidentID string) ([]incident.IncidentActivity, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT action,actor_type,COALESCE(metadata->>'actor',''),COALESCE(metadata->>'reason',''),occurred_at
		FROM eventing.audit_logs
		WHERE resource_type='incident' AND resource_id=$1
		ORDER BY occurred_at,id`, incidentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []incident.IncidentActivity{}
	for rows.Next() {
		var item incident.IncidentActivity
		if err = rows.Scan(&item.Action, &item.ActorType, &item.Actor, &item.Reason, &item.OccurredAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ListIncidentEvidenceLogs(ctx context.Context, item incident.Incident, limit int) ([]incident.EvidenceLog, error) {
	if item.SourceType == "performance_budget" {
		return []incident.EvidenceLog{}, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	since := item.OpenedAt.Add(-time.Duration(item.WindowMinutes) * time.Minute)
	until := item.OpenedAt.Add(time.Minute)
	query := `SELECT id,occurred_at,service,level,event,message,COALESCE(trace_id,''),COALESCE(run_id,''),COALESCE(error_code,'') FROM ops.system_logs WHERE occurred_at >= $1 AND occurred_at <= $2 AND level=$3`
	args := []any{since, until, item.Level}
	if item.Service != "" {
		args = append(args, item.Service)
		query += ` AND service=$` + placeholder(len(args))
	}
	if item.EventPrefix != "" {
		args = append(args, item.EventPrefix+"%")
		query += ` AND event LIKE $` + placeholder(len(args))
	}
	args = append(args, limit)
	query += ` ORDER BY occurred_at,id LIMIT $` + placeholder(len(args))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []incident.EvidenceLog{}
	for rows.Next() {
		var item incident.EvidenceLog
		if err = rows.Scan(&item.ID, &item.OccurredAt, &item.Service, &item.Level, &item.Event, &item.Message, &item.TraceID, &item.RunID, &item.ErrorCode); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func queueIncidentNotificationsTx(ctx context.Context, tx *sql.Tx, item incident.Incident, transition string, now time.Time) (int, error) {
	rows, err := tx.QueryContext(ctx, alertSubscriptionSelect+`
		WHERE s.enabled=TRUE
			AND (s.rule_id IS NULL OR s.rule_id=NULLIF($1,'')::uuid)
			AND (s.minimum_severity='warning' OR $2='critical')
			AND (($3 IN ('opened','escalated') AND s.notify_on_open) OR ($3='resolved' AND s.notify_on_resolved))
		ORDER BY s.id`, item.RuleID, item.Severity, transition)
	if err != nil {
		return 0, err
	}
	subscriptions := []incident.AlertSubscription{}
	for rows.Next() {
		subscription, scanErr := scanAlertSubscription(rows)
		if scanErr != nil {
			rows.Close()
			return 0, scanErr
		}
		subscriptions = append(subscriptions, subscription)
	}
	if err = rows.Close(); err != nil {
		return 0, err
	}
	queued := 0
	for _, subscription := range subscriptions {
		var exists bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ops.incident_notifications WHERE incident_id=$1 AND subscription_id=$2 AND transition=$3)`, item.ID, subscription.ID, transition).Scan(&exists); err != nil {
			return queued, err
		}
		if exists {
			continue
		}
		notificationID, idErr := id.New()
		if idErr != nil {
			return queued, idErr
		}
		deliveryID := ""
		notificationStatus := "sent"
		if subscription.Channel == "email" {
			deliveryID, idErr = id.New()
			if idErr != nil {
				return queued, idErr
			}
			eventID, eventErr := id.New()
			if eventErr != nil {
				return queued, eventErr
			}
			template, subject, body := incidentEmailContent(item, transition)
			if _, err = tx.ExecContext(ctx, `
				INSERT INTO app.email_deliveries
					(id,actor_id,resource_type,resource_id,template,recipient_email,subject,body_text,status,available_at,created_at,updated_at)
				VALUES ($1,NULL,'incident',$2,$3,$4,$5,$6,'queued',$7,$7,$7)`, deliveryID, item.ID, template, subscription.Target, subject, body, now); err != nil {
				return queued, err
			}
			payload, _ := json.Marshal(map[string]string{"delivery_id": deliveryID})
			if _, err = tx.ExecContext(ctx, `INSERT INTO eventing.outbox_events (id,aggregate_type,aggregate_id,event_type,event_version,payload,occurred_at,trace_id,traceparent,tracestate) VALUES ($1,'email_delivery',$2,'email.deliver.v1',1,$3,$4,NULLIF($5,''),NULLIF($6,''),NULLIF($7,''))`, eventID, deliveryID, payload, now, outboxTraceID(ctx), outboxTraceParent(ctx), outboxTraceState(ctx)); err != nil {
				return queued, err
			}
			notificationStatus = "queued"
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO ops.incident_notifications (
			id,incident_id,subscription_id,transition,channel,target,delivery_id,status,attempts,sent_at,created_at
		) VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,'')::uuid,$8,CASE WHEN $8='sent' THEN 1 ELSE 0 END,CASE WHEN $8='sent' THEN $9::timestamptz END,$9)`,
			notificationID, item.ID, subscription.ID, transition, subscription.Channel, subscription.Target, deliveryID, notificationStatus, now); err != nil {
			return queued, err
		}
		auditDeliveryID := deliveryID
		if auditDeliveryID == "" {
			auditDeliveryID = notificationID
		}
		if err = insertIncidentAudit(ctx, tx, "system", "alert-evaluator", "incident.notification."+notificationStatus, "incident", item.ID, transition, now, map[string]any{"delivery_id": auditDeliveryID, "subscription_id": subscription.ID, "channel": subscription.Channel}); err != nil {
			return queued, err
		}
		queued++
	}
	return queued, nil
}

func incidentEmailContent(item incident.Incident, transition string) (string, string, string) {
	if item.SourceType == "performance_budget" {
		module := "全部模块"
		if item.Service != "" {
			module = item.Service
		}
		forecast := strings.HasSuffix(item.EventPrefix, ".projected_exceeded")
		if transition == "resolved" && forecast {
			return "performance.budget.forecast.resolved.v1", "[伴AI] 成本预算预测风险已解除：" + item.RuleName, fmt.Sprintf("成本预算预测已回到安全范围\n\n预算：%s\n范围：%s\n当前使用率：%d%%\n解决方式：%s\n事故 ID：%s\n", item.RuleName, module, item.ObservedValue, item.Resolution, item.ID)
		}
		if transition == "resolved" {
			return "performance.budget.resolved.v1", "[伴AI] 成本预算已恢复：" + item.RuleName, fmt.Sprintf("成本预算已恢复到安全范围\n\n预算：%s\n范围：%s\n当前使用率：%d%%\n解决方式：%s\n事故 ID：%s\n", item.RuleName, module, item.ObservedValue, item.Resolution, item.ID)
		}
		if transition == "escalated" {
			return "performance.budget.exceeded.v1", "[伴AI] 成本预算已超限：" + item.RuleName, fmt.Sprintf("成本预算事故已升级为严重级别\n\n预算：%s\n范围：%s\n当前使用率：%d%%\n事故 ID：%s\n", item.RuleName, module, item.ObservedValue, item.ID)
		}
		if item.Severity == "critical" {
			return "performance.budget.exceeded.v1", "[伴AI] 成本预算已超限：" + item.RuleName, fmt.Sprintf("成本预算已超过上限\n\n预算：%s\n范围：%s\n当前使用率：%d%%\n事故 ID：%s\n", item.RuleName, module, item.ObservedValue, item.ID)
		}
		if forecast {
			return "performance.budget.forecast.warning.v1", "[伴AI] 成本预算预计将超限：" + item.RuleName, fmt.Sprintf("根据近期实际消耗速度，成本预算预计将在当前周期内超限\n\n预算：%s\n范围：%s\n%s\n事故 ID：%s\n\n此预测不会自动修改生产模型，请先评估节流或模型切换方案。\n", item.RuleName, module, item.Summary, item.ID)
		}
		return "performance.budget.warning.v1", "[伴AI] 成本预算预警：" + item.RuleName, fmt.Sprintf("成本预算达到预警线\n\n预算：%s\n范围：%s\n当前使用率：%d%%\n事故 ID：%s\n", item.RuleName, module, item.ObservedValue, item.ID)
	}
	if transition == "resolved" {
		return "incident.resolved.v1", "[伴AI] 事故已恢复：" + item.Title, fmt.Sprintf("事故已恢复\n\n标题：%s\n级别：%s\n服务：%s\n解决方式：%s\n事故 ID：%s\n", item.Title, item.Severity, displayIncidentService(item.Service), item.Resolution, item.ID)
	}
	return "incident.opened.v1", "[伴AI] 新事故：" + item.Title, fmt.Sprintf("检测到新的系统事故\n\n标题：%s\n级别：%s\n服务：%s\n观测值：%d（阈值 %d）\n窗口：%d 分钟\n事故 ID：%s\n", item.Title, item.Severity, displayIncidentService(item.Service), item.ObservedValue, item.Threshold, item.WindowMinutes, item.ID)
}

func displayIncidentService(value string) string {
	if value == "" {
		return "全部服务"
	}
	return value
}

func maskIncidentEmail(value string) string {
	parts := strings.Split(strings.TrimSpace(value), "@")
	if len(parts) != 2 {
		return "***"
	}
	local := []rune(parts[0])
	if len(local) == 0 {
		return "***@" + parts[1]
	}
	return string(local[0]) + "***@" + parts[1]
}

const alertRuleSelect = `SELECT id::text,name,description,COALESCE(service,''),level,COALESCE(event_prefix,''),window_minutes,threshold,severity,enabled,created_by,updated_by,created_at,updated_at FROM ops.alert_rules`

func scanAlertRule(row rowScanner) (incident.AlertRule, error) {
	var item incident.AlertRule
	err := row.Scan(&item.ID, &item.Name, &item.Description, &item.Service, &item.Level, &item.EventPrefix, &item.WindowMinutes, &item.Threshold, &item.Severity, &item.Enabled, &item.CreatedBy, &item.UpdatedBy, &item.CreatedAt, &item.UpdatedAt)
	return item, err
}

const alertSubscriptionSelect = `SELECT s.id::text,s.name,COALESCE(s.rule_id::text,''),COALESCE(r.name,''),s.channel,s.target,s.minimum_severity,s.notify_on_open,s.notify_on_resolved,s.enabled,s.created_by,s.updated_by,s.created_at,s.updated_at FROM ops.alert_subscriptions s LEFT JOIN ops.alert_rules r ON r.id=s.rule_id`

func scanAlertSubscription(row rowScanner) (incident.AlertSubscription, error) {
	var item incident.AlertSubscription
	err := row.Scan(&item.ID, &item.Name, &item.RuleID, &item.RuleName, &item.Channel, &item.Target, &item.MinimumSeverity, &item.NotifyOnOpen, &item.NotifyOnResolved, &item.Enabled, &item.CreatedBy, &item.UpdatedBy, &item.CreatedAt, &item.UpdatedAt)
	return item, err
}

const incidentSelect = `SELECT i.id::text,COALESCE(i.rule_id::text,''),COALESCE(r.name,i.evidence->>'budget_name',i.title),i.source_type,COALESCE(i.source_id::text,''),i.status,i.severity,i.title,i.summary,COALESCE(i.service,''),i.level,COALESCE(i.event_prefix,''),i.observed_value,i.threshold,i.window_minutes,i.window_started_at,i.window_ended_at,i.evidence,i.opened_at,i.acknowledged_at,COALESCE(i.acknowledged_by,''),i.resolved_at,COALESCE(i.resolved_by,''),COALESCE(i.resolution,''),i.updated_at FROM ops.incidents i LEFT JOIN ops.alert_rules r ON r.id=i.rule_id`

func scanIncident(row rowScanner) (incident.Incident, error) {
	var item incident.Incident
	err := row.Scan(&item.ID, &item.RuleID, &item.RuleName, &item.SourceType, &item.SourceID, &item.Status, &item.Severity, &item.Title, &item.Summary, &item.Service, &item.Level, &item.EventPrefix, &item.ObservedValue, &item.Threshold, &item.WindowMinutes, &item.WindowStartedAt, &item.WindowEndedAt, &item.Evidence, &item.OpenedAt, &item.AcknowledgedAt, &item.AcknowledgedBy, &item.ResolvedAt, &item.ResolvedBy, &item.Resolution, &item.UpdatedAt)
	return item, err
}

func insertIncidentAudit(ctx context.Context, tx *sql.Tx, actorType, actor, action, resourceType, resourceID, reason string, now time.Time, extra map[string]any) error {
	metadata := map[string]any{"actor": strings.TrimSpace(actor), "reason": strings.TrimSpace(reason)}
	for key, value := range extra {
		metadata[key] = value
	}
	payload, _ := json.Marshal(metadata)
	_, err := tx.ExecContext(ctx, `INSERT INTO eventing.audit_logs (actor_type,actor_id,action,resource_type,resource_id,metadata,occurred_at) VALUES ($1,NULL,$2,$3,$4,$5,$6)`, actorType, action, resourceType, resourceID, payload, now)
	return err
}
