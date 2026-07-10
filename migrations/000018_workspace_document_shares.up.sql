CREATE TABLE workspace_document_shares (
    workspace_id BINARY(16) NOT NULL,
    document_id BINARY(16) NOT NULL,
    shared_by BINARY(16) NOT NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (workspace_id,document_id),
    KEY idx_workspace_document_shares_document (document_id,created_at),
    KEY idx_workspace_document_shares_shared_by (shared_by,created_at),
    CONSTRAINT fk_workspace_document_shares_workspace FOREIGN KEY (workspace_id) REFERENCES workspaces(id),
    CONSTRAINT fk_workspace_document_shares_document FOREIGN KEY (document_id) REFERENCES documents(id),
    CONSTRAINT fk_workspace_document_shares_shared_by FOREIGN KEY (shared_by) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
