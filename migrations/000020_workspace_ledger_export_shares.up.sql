CREATE TABLE workspace_ledger_export_shares (
    workspace_id BINARY(16) NOT NULL,
    export_id BINARY(16) NOT NULL,
    shared_by BINARY(16) NOT NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (workspace_id,export_id),
    KEY idx_workspace_ledger_export_shares_export (export_id,created_at),
    KEY idx_workspace_ledger_export_shares_shared_by (shared_by,created_at),
    CONSTRAINT fk_workspace_ledger_export_shares_workspace FOREIGN KEY (workspace_id) REFERENCES workspaces(id),
    CONSTRAINT fk_workspace_ledger_export_shares_export FOREIGN KEY (export_id) REFERENCES ledger_exports(id),
    CONSTRAINT fk_workspace_ledger_export_shares_shared_by FOREIGN KEY (shared_by) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
