CREATE VIEW app.model_usage_observations AS
SELECT
    'legacy_chat'::TEXT AS source_type,
    usage.generation_job_id::TEXT AS execution_id,
    ROW_NUMBER() OVER (
        PARTITION BY usage.generation_job_id
        ORDER BY usage.id
    )::BIGINT AS call_index,
    usage.user_id,
    'succeeded'::TEXT AS status,
    usage.provider::TEXT AS provider,
    usage.model::TEXT AS requested_model,
    usage.model::TEXT AS returned_model,
    ''::TEXT AS model_config_version,
    'legacy_responder'::TEXT AS role,
    'legacy_chat'::TEXT AS graph_node,
    usage.input_tokens::BIGINT AS prompt_tokens,
    usage.output_tokens::BIGINT AS completion_tokens,
    0::BIGINT AS cached_tokens,
    0::BIGINT AS reasoning_tokens,
    usage.estimated_cost_micros::BIGINT AS cost_micros,
    usage.latency_ms::BIGINT AS latency_ms,
    usage.created_at AS observed_at
FROM app.model_usage_records AS usage

UNION ALL

SELECT
    'agent_graph'::TEXT AS source_type,
    run.id::TEXT AS execution_id,
    model_call.call_index::BIGINT,
    run.user_id,
    COALESCE(NULLIF(model_call.item->>'status', ''), 'unknown') AS status,
    COALESCE(NULLIF(model_call.item->>'provider', ''), 'unknown') AS provider,
    COALESCE(model_call.item->>'requested_model', '') AS requested_model,
    COALESCE(model_call.item->>'returned_model', '') AS returned_model,
    COALESCE(
        NULLIF(run.output #>> '{model,manifest,config_version}', ''),
        NULLIF(run.output #>> '{model,default_config_version}', ''),
        ''
    ) AS model_config_version,
    COALESCE(model_call.item->>'role', '') AS role,
    COALESCE(model_call.item->>'graph_node', '') AS graph_node,
    CASE
        WHEN (model_call.item->>'prompt_tokens') ~ '^[0-9]+$'
            THEN LEAST(
                (model_call.item->>'prompt_tokens')::NUMERIC,
                9223372036854775807
            )::BIGINT
        ELSE 0
    END AS prompt_tokens,
    CASE
        WHEN (model_call.item->>'completion_tokens') ~ '^[0-9]+$'
            THEN LEAST(
                (model_call.item->>'completion_tokens')::NUMERIC,
                9223372036854775807
            )::BIGINT
        ELSE 0
    END AS completion_tokens,
    CASE
        WHEN (model_call.item->>'cached_tokens') ~ '^[0-9]+$'
            THEN LEAST(
                (model_call.item->>'cached_tokens')::NUMERIC,
                9223372036854775807
            )::BIGINT
        ELSE 0
    END AS cached_tokens,
    CASE
        WHEN (model_call.item->>'reasoning_tokens') ~ '^[0-9]+$'
            THEN LEAST(
                (model_call.item->>'reasoning_tokens')::NUMERIC,
                9223372036854775807
            )::BIGINT
        ELSE 0
    END AS reasoning_tokens,
    CASE
        WHEN (model_call.item->>'cost_micros') ~ '^[0-9]+$'
            THEN LEAST(
                (model_call.item->>'cost_micros')::NUMERIC,
                9223372036854775807
            )::BIGINT
        ELSE 0
    END AS cost_micros,
    CASE
        WHEN (model_call.item->>'latency_ms') ~ '^[0-9]+$'
            THEN LEAST(
                (model_call.item->>'latency_ms')::NUMERIC,
                9223372036854775807
            )::BIGINT
        ELSE 0
    END AS latency_ms,
    COALESCE(run.completed_at, run.updated_at) AS observed_at
FROM agent.runs AS run
CROSS JOIN LATERAL JSONB_ARRAY_ELEMENTS(
    CASE
        WHEN JSONB_TYPEOF(run.output #> '{model,calls}') = 'array'
            THEN run.output #> '{model,calls}'
        ELSE '[]'::JSONB
    END
) WITH ORDINALITY AS model_call(item, call_index)

UNION ALL

SELECT
    'skill'::TEXT AS source_type,
    run.id::TEXT AS execution_id,
    step.sequence_no::BIGINT AS call_index,
    run.user_id,
    step.status::TEXT AS status,
    COALESCE(NULLIF(step.output_json #>> '{model_usage,provider}', ''), 'unknown') AS provider,
    COALESCE(step.output_json #>> '{model_usage,requested_model}', '') AS requested_model,
    COALESCE(step.output_json #>> '{model_usage,returned_model}', '') AS returned_model,
    COALESCE(step.output_json #>> '{model_usage,config_version}', '') AS model_config_version,
    run.skill_name::TEXT AS role,
    'skill'::TEXT AS graph_node,
    CASE
        WHEN (step.output_json #>> '{model_usage,prompt_tokens}') ~ '^[0-9]+$'
            THEN LEAST(
                (step.output_json #>> '{model_usage,prompt_tokens}')::NUMERIC,
                9223372036854775807
            )::BIGINT
        ELSE 0
    END AS prompt_tokens,
    CASE
        WHEN (step.output_json #>> '{model_usage,completion_tokens}') ~ '^[0-9]+$'
            THEN LEAST(
                (step.output_json #>> '{model_usage,completion_tokens}')::NUMERIC,
                9223372036854775807
            )::BIGINT
        ELSE 0
    END AS completion_tokens,
    0::BIGINT AS cached_tokens,
    0::BIGINT AS reasoning_tokens,
    CASE
        WHEN (step.output_json #>> '{model_usage,cost_micros}') ~ '^[0-9]+$'
            THEN LEAST(
                (step.output_json #>> '{model_usage,cost_micros}')::NUMERIC,
                9223372036854775807
            )::BIGINT
        ELSE 0
    END AS cost_micros,
    CASE
        WHEN (step.output_json #>> '{model_usage,latency_ms}') ~ '^[0-9]+$'
            THEN LEAST(
                (step.output_json #>> '{model_usage,latency_ms}')::NUMERIC,
                9223372036854775807
            )::BIGINT
        ELSE 0
    END AS latency_ms,
    COALESCE(step.completed_at, step.started_at) AS observed_at
FROM app.skill_run_steps AS step
JOIN app.skill_runs AS run ON run.id = step.run_id
WHERE step.state = 'execute'
    AND JSONB_TYPEOF(step.output_json->'model_usage') = 'object';

CREATE INDEX model_usage_records_observed_idx
    ON app.model_usage_records (created_at);

CREATE INDEX agent_runs_model_usage_observed_idx
    ON agent.runs ((COALESCE(completed_at, updated_at)))
    WHERE JSONB_TYPEOF(output #> '{model,calls}') = 'array';

CREATE INDEX skill_run_steps_model_usage_observed_idx
    ON app.skill_run_steps ((COALESCE(completed_at, started_at)))
    WHERE state = 'execute'
        AND JSONB_TYPEOF(output_json->'model_usage') = 'object';

COMMENT ON VIEW app.model_usage_observations IS
    'Unified persisted model usage from legacy chat, Agent Graph calls, and model-backed Skills.';
