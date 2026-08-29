ALTER TABLE app.documents
    ADD COLUMN source_ir_version VARCHAR(64),
    ADD COLUMN source_ir JSONB;
