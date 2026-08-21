CREATE SCHEMA IF NOT EXISTS app;
CREATE SCHEMA IF NOT EXISTS eventing;
CREATE SCHEMA IF NOT EXISTS agent;
CREATE SCHEMA IF NOT EXISTS langgraph;

CREATE TABLE agent.runs (
    id UUID PRIMARY KEY,
    thread_id TEXT NOT NULL UNIQUE,
    user_id UUID NOT NULL,
    conversation_id UUID NOT NULL,
    character_id UUID NOT NULL,
    module_key TEXT NOT NULL CHECK (module_key IN ('companion', 'life', 'work')),
    graph_name TEXT NOT NULL,
    graph_version TEXT NOT NULL,
    status TEXT NOT NULL CHECK (
        status IN (
            'accepted',
            'queued',
            'running',
            'waiting_approval',
            'waiting_tool',
            'completed',
            'failed',
            'cancel_requested',
            'cancelled'
        )
    ),
    idempotency_key TEXT,
    input JSONB NOT NULL DEFAULT '{}'::JSONB,
    output JSONB,
    error_code TEXT,
    error_message TEXT,
    available_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    lease_owner TEXT,
    lease_expires_at TIMESTAMPTZ,
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX agent_runs_user_idempotency_uq
    ON agent.runs (user_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

CREATE INDEX agent_runs_dispatch_idx
    ON agent.runs (status, available_at, created_at)
    WHERE status IN ('accepted', 'queued');

CREATE INDEX agent_runs_conversation_idx
    ON agent.runs (conversation_id, created_at DESC);

CREATE TABLE agent.interrupts (
    id UUID PRIMARY KEY,
    run_id UUID NOT NULL REFERENCES agent.runs (id) ON DELETE CASCADE,
    interrupt_key TEXT NOT NULL,
    interrupt_type TEXT NOT NULL CHECK (
        interrupt_type IN ('approval', 'clarification', 'tool_result')
    ),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (
        status IN ('pending', 'resolved', 'cancelled', 'expired')
    ),
    request JSONB NOT NULL,
    resolution JSONB,
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resolved_at TIMESTAMPTZ,
    UNIQUE (run_id, interrupt_key)
);

CREATE INDEX agent_interrupts_pending_idx
    ON agent.interrupts (run_id, created_at)
    WHERE status = 'pending';

CREATE TABLE agent.tool_calls (
    id UUID PRIMARY KEY,
    run_id UUID NOT NULL REFERENCES agent.runs (id) ON DELETE CASCADE,
    call_key TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    risk_level TEXT NOT NULL CHECK (risk_level IN ('none', 'low', 'medium', 'high')),
    status TEXT NOT NULL CHECK (
        status IN ('proposed', 'approved', 'running', 'succeeded', 'failed', 'cancelled')
    ),
    arguments JSONB NOT NULL,
    result JSONB,
    error_code TEXT,
    error_message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at TIMESTAMPTZ,
    UNIQUE (run_id, call_key)
);

CREATE INDEX agent_tool_calls_status_idx
    ON agent.tool_calls (status, created_at);

CREATE TABLE agent.run_events (
    id BIGSERIAL PRIMARY KEY,
    run_id UUID NOT NULL REFERENCES agent.runs (id) ON DELETE CASCADE,
    sequence_no BIGINT NOT NULL CHECK (sequence_no > 0),
    event_type TEXT NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (run_id, sequence_no)
);

CREATE INDEX agent_run_events_stream_idx
    ON agent.run_events (run_id, sequence_no);
