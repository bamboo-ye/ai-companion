package postgresstore

import (
	"context"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/opslog"
	"github.com/windcry1/ai-companion/internal/platform/tracectx"
)

var _ opslog.Store = (*Store)(nil)

func (s *Store) AppendSystemLog(ctx context.Context, entry opslog.Entry) error {
	if len(entry.Attributes) == 0 {
		entry.Attributes = []byte(`{}`)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO ops.system_logs
			(occurred_at,service,environment,level,event,message,trace_id,span_id,trace_flags,run_id,node,error_code,attributes)
		VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,''),NULLIF($8,''),NULLIF($9,''),NULLIF($10,''),NULLIF($11,''),NULLIF($12,''),$13)`,
		entry.OccurredAt, entry.Service, entry.Environment, entry.Level, entry.Event, entry.Message,
		entry.TraceID, entry.SpanID, entry.TraceFlags, entry.RunID, entry.Node, entry.ErrorCode, entry.Attributes,
	)
	return err
}

func (s *Store) QuerySystemLogs(ctx context.Context, filter opslog.Filter) (opslog.Page, error) {
	filter, err := opslog.NormalizeFilter(filter, time.Now().UTC())
	if err != nil {
		return opslog.Page{}, err
	}
	where, args := systemLogWhere(filter, true)
	args = append(args, filter.Limit+1)
	rows, err := s.db.QueryContext(ctx, systemLogSelect+where+` ORDER BY occurred_at DESC,id DESC LIMIT $`+placeholder(len(args)), args...)
	if err != nil {
		return opslog.Page{}, err
	}
	defer rows.Close()
	page := opslog.Page{Items: []opslog.Entry{}, Services: []string{}, Source: "postgres"}
	for rows.Next() {
		var entry opslog.Entry
		if err = rows.Scan(
			&entry.ID, &entry.OccurredAt, &entry.Service, &entry.Environment, &entry.Level,
			&entry.Event, &entry.Message, &entry.TraceID, &entry.SpanID, &entry.TraceFlags, &entry.RunID, &entry.Node,
			&entry.ErrorCode, &entry.Attributes,
		); err != nil {
			return opslog.Page{}, err
		}
		if entry.AgentTraceID == "" && entry.RunID != "" {
			entry.AgentTraceID = entry.TraceID
			if entry.AgentTraceID == "" {
				entry.AgentTraceID = tracectx.AgentRunTraceID(entry.RunID)
			}
		}
		page.Items = append(page.Items, entry)
	}
	if err = rows.Err(); err != nil {
		return opslog.Page{}, err
	}
	if len(page.Items) > filter.Limit {
		page.Items = page.Items[:filter.Limit]
		page.NextBeforeID = page.Items[len(page.Items)-1].ID
	}

	summaryWhere, summaryArgs := systemLogWhere(filter, false)
	err = s.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
			COUNT(*) FILTER (WHERE level='ERROR'),
			COUNT(*) FILTER (WHERE level='WARN'),
			COUNT(DISTINCT service)
		FROM ops.system_logs`+summaryWhere, summaryArgs...,
	).Scan(&page.Summary.Total, &page.Summary.Errors, &page.Summary.Warnings, &page.Summary.Services)
	if err != nil {
		return opslog.Page{}, err
	}
	serviceRows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT service FROM ops.system_logs
		WHERE occurred_at >= $1 AND occurred_at <= $2 ORDER BY service`, filter.Since, filter.Until)
	if err != nil {
		return opslog.Page{}, err
	}
	defer serviceRows.Close()
	for serviceRows.Next() {
		var service string
		if err = serviceRows.Scan(&service); err != nil {
			return opslog.Page{}, err
		}
		page.Services = append(page.Services, service)
	}
	return page, serviceRows.Err()
}

const systemLogSelect = `
	SELECT id,occurred_at,service,environment,level,event,message,
		COALESCE(trace_id,''),COALESCE(span_id,''),COALESCE(trace_flags,''),COALESCE(run_id,''),COALESCE(node,''),COALESCE(error_code,''),attributes
	FROM ops.system_logs`

func systemLogWhere(filter opslog.Filter, includeBefore bool) (string, []any) {
	clauses := []string{"occurred_at >= $1", "occurred_at <= $2"}
	args := []any{filter.Since, filter.Until}
	add := func(column, value string) {
		if value == "" {
			return
		}
		args = append(args, value)
		clauses = append(clauses, column+`=$`+placeholder(len(args)))
	}
	add("service", filter.Service)
	add("level", filter.Level)
	add("trace_id", filter.TraceID)
	add("run_id", filter.RunID)
	if filter.Query != "" {
		args = append(args, "%"+filter.Query+"%")
		position := `$` + placeholder(len(args))
		clauses = append(clauses, `(event ILIKE `+position+` OR message ILIKE `+position+` OR COALESCE(error_code,'') ILIKE `+position+` OR attributes::text ILIKE `+position+`)`)
	}
	if includeBefore && filter.BeforeID > 0 {
		args = append(args, filter.BeforeID)
		clauses = append(clauses, `id < $`+placeholder(len(args)))
	}
	return ` WHERE ` + strings.Join(clauses, ` AND `), args
}
