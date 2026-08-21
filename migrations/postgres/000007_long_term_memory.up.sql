CREATE TABLE app.long_term_memories (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES app.users (id),
    memory_type TEXT NOT NULL CHECK (
        memory_type IN ('preference', 'relationship', 'goal', 'commitment', 'experience', 'fact')
    ),
    content VARCHAR(2000) NOT NULL,
    normalized_hash CHAR(64) NOT NULL CHECK (LENGTH(normalized_hash) = 64),
    source_conversation_id UUID REFERENCES app.conversations (id),
    source_message_id UUID REFERENCES app.messages (id),
    confidence NUMERIC(5, 4) NOT NULL CHECK (confidence >= 0 AND confidence <= 1),
    importance NUMERIC(5, 4) NOT NULL DEFAULT 0.5000 CHECK (
        importance >= 0 AND importance <= 1
    ),
    sensitivity TEXT NOT NULL DEFAULT 'normal' CHECK (
        sensitivity IN ('normal', 'personal', 'sensitive')
    ),
    pinned BOOLEAN NOT NULL DEFAULT FALSE,
    status TEXT NOT NULL DEFAULT 'active' CHECK (
        status IN ('active', 'deleted', 'superseded')
    ),
    valid_from TIMESTAMPTZ NOT NULL,
    valid_to TIMESTAMPTZ,
    supersedes_id UUID REFERENCES app.long_term_memories (id),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CHECK (valid_to IS NULL OR valid_to >= valid_from)
);

CREATE INDEX long_term_memories_user_status_idx
    ON app.long_term_memories (user_id, status, pinned, updated_at);

CREATE INDEX long_term_memories_user_hash_idx
    ON app.long_term_memories (user_id, normalized_hash, status);
