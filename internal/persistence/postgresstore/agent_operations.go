package postgresstore

import (
	"context"

	"github.com/windcry1/ai-companion/internal/agent"
)

var _ agent.OperationsStore = (*Store)(nil)

func (s *Store) ListAgentRuns(ctx context.Context, filter agent.RunOperationsFilter) ([]agent.RunSummary, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, agentRunOperationsQuery, filter.Status, filter.Module,
		filter.ErrorCode, filter.Query, filter.CreatedFrom, filter.CreatedTo,
		filter.CursorCreatedAt, nullableString(filter.CursorID), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]agent.RunSummary, 0, limit)
	for rows.Next() {
		var item agent.RunSummary
		if err = scanAgentRunSummary(rows, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ListAgentToolCalls(ctx context.Context, runID string) ([]agent.ToolCallSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id::text,run_id::text,tool_name,risk_level,status,
			COALESCE(error_code,''),COALESCE(error_message,''),created_at,updated_at,completed_at
		FROM agent.tool_calls
		WHERE run_id=$1
		ORDER BY created_at,id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]agent.ToolCallSummary, 0)
	for rows.Next() {
		var item agent.ToolCallSummary
		if err = rows.Scan(&item.ID, &item.RunID, &item.ToolName, &item.RiskLevel,
			&item.Status, &item.ErrorCode, &item.ErrorMessage, &item.CreatedAt,
			&item.UpdatedAt, &item.CompletedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

const agentRunOperationsQuery = `
	SELECT r.id::text,r.thread_id,r.user_id::text,r.conversation_id::text,
		r.module_key,r.graph_name,r.graph_version,COALESCE(r.agent_definition_key,''),
		COALESCE(r.agent_definition_version_id::text,''),COALESCE(r.agent_definition_version,0),
		COALESCE(r.agent_definition_revision,0),COALESCE(r.agent_definition_fingerprint,''),
		COALESCE(r.model_profile_key,''),
		COALESCE(r.model_profile_version_id::text,''),COALESCE(r.model_profile_revision,0),
		COALESCE(r.model_profile_config_version,''),COALESCE(r.model_profile_fingerprint,''),r.status,
		COALESCE(r.output #>> '{observability,node_trace,-1,node}',''),r.revision,
		COALESCE(usage.model_calls,0),COALESCE(usage.prompt_tokens,0),
		COALESCE(usage.completion_tokens,0),COALESCE(usage.cost_micros,0),
		COALESCE(tools.tool_calls,0),COALESCE(r.error_code,''),
		COALESCE(r.error_message,''),
		GREATEST(0,(EXTRACT(EPOCH FROM (COALESCE(r.completed_at,r.updated_at)-r.created_at))*1000)::bigint),
		r.deadline_at,r.created_at,r.updated_at,r.completed_at
	FROM agent.runs r
	LEFT JOIN LATERAL (
		SELECT COUNT(*)::bigint AS model_calls,
			COALESCE(SUM(CASE WHEN jsonb_typeof(call->'prompt_tokens')='number' THEN (call->>'prompt_tokens')::bigint ELSE 0 END),0)::bigint AS prompt_tokens,
			COALESCE(SUM(CASE WHEN jsonb_typeof(call->'completion_tokens')='number' THEN (call->>'completion_tokens')::bigint ELSE 0 END),0)::bigint AS completion_tokens,
			COALESCE(SUM(CASE WHEN jsonb_typeof(call->'cost_micros')='number' THEN (call->>'cost_micros')::bigint ELSE 0 END),0)::bigint AS cost_micros
		FROM jsonb_array_elements(
			CASE WHEN jsonb_typeof(r.output #> '{model,calls}')='array'
				THEN r.output #> '{model,calls}' ELSE '[]'::jsonb END
		) call
	) usage ON TRUE
	LEFT JOIN LATERAL (
		SELECT COUNT(*)::bigint AS tool_calls FROM agent.tool_calls tc WHERE tc.run_id=r.id
	) tools ON TRUE
	WHERE ($1='' OR r.status=$1)
		AND ($2='' OR r.module_key=$2)
		AND ($3='' OR COALESCE(r.error_code,'')=$3)
		AND ($4='' OR r.id::text=$4 OR r.thread_id=$4 OR r.conversation_id::text=$4)
		AND ($5::timestamptz IS NULL OR r.created_at >= $5)
		AND ($6::timestamptz IS NULL OR r.created_at <= $6)
		AND ($7::timestamptz IS NULL OR (r.created_at,r.id) < ($7,$8::uuid))
	ORDER BY r.created_at DESC,r.id DESC
	LIMIT $9`

type agentRunSummaryScanner interface {
	Scan(...any) error
}

func scanAgentRunSummary(row agentRunSummaryScanner, item *agent.RunSummary) error {
	return row.Scan(&item.ID, &item.ThreadID, &item.UserID, &item.ConversationID,
		&item.Module, &item.GraphName, &item.GraphVersion, &item.AgentDefinitionKey,
		&item.AgentDefinitionVersionID, &item.AgentDefinitionVersion,
		&item.AgentDefinitionRevision, &item.AgentDefinitionFingerprint,
		&item.ModelProfileKey,
		&item.ModelVersionID, &item.ModelRevision, &item.ModelConfig,
		&item.ModelFingerprint, &item.Status,
		&item.CurrentNode, &item.Revision, &item.ModelCalls, &item.PromptTokens,
		&item.CompletionTokens, &item.CostMicros, &item.ToolCalls, &item.ErrorCode,
		&item.ErrorMessage, &item.DurationMS, &item.DeadlineAt, &item.CreatedAt,
		&item.UpdatedAt, &item.CompletedAt)
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
