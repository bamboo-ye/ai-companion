CREATE TABLE workspace_generated_file_shares (
    workspace_id BINARY(16) NOT NULL,
    file_id BINARY(16) NOT NULL,
    shared_by BINARY(16) NOT NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (workspace_id,file_id),
    KEY idx_workspace_generated_file_shares_file (file_id,created_at),
    KEY idx_workspace_generated_file_shares_shared_by (shared_by,created_at),
    CONSTRAINT fk_workspace_generated_file_shares_workspace FOREIGN KEY (workspace_id) REFERENCES workspaces(id),
    CONSTRAINT fk_workspace_generated_file_shares_file FOREIGN KEY (file_id) REFERENCES generated_files(id),
    CONSTRAINT fk_workspace_generated_file_shares_shared_by FOREIGN KEY (shared_by) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
