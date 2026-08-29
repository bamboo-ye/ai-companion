ALTER TABLE documents
    ADD COLUMN source_ir_version VARCHAR(64) NULL AFTER parser_version,
    ADD COLUMN source_ir JSON NULL AFTER source_ir_version;
