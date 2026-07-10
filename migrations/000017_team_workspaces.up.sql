CREATE TABLE workspaces (
    id BINARY(16) NOT NULL,
    name VARCHAR(160) NOT NULL,
    owner_id BINARY(16) NOT NULL,
    status ENUM('active','disabled','deleted') NOT NULL DEFAULT 'active',
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    KEY idx_workspaces_owner_status (owner_id,status,created_at),
    CONSTRAINT fk_workspaces_owner FOREIGN KEY (owner_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE workspace_members (
    workspace_id BINARY(16) NOT NULL,
    user_id BINARY(16) NOT NULL,
    role ENUM('owner','admin','member') NOT NULL DEFAULT 'member',
    status ENUM('active','disabled','removed') NOT NULL DEFAULT 'active',
    joined_at TIMESTAMP(6) NOT NULL,
    updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (workspace_id,user_id),
    KEY idx_workspace_members_user (user_id,status,joined_at),
    CONSTRAINT fk_workspace_members_workspace FOREIGN KEY (workspace_id) REFERENCES workspaces(id),
    CONSTRAINT fk_workspace_members_user FOREIGN KEY (user_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE workspace_invitations (
    id BINARY(16) NOT NULL,
    workspace_id BINARY(16) NOT NULL,
    email VARCHAR(320) NOT NULL,
    role ENUM('admin','member') NOT NULL DEFAULT 'member',
    status ENUM('pending','accepted','revoked','expired') NOT NULL DEFAULT 'pending',
    invited_by BINARY(16) NOT NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    expires_at TIMESTAMP(6) NOT NULL,
    accepted_at TIMESTAMP(6) NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_workspace_pending_invitation (workspace_id,email,status),
    KEY idx_workspace_invitations_workspace (workspace_id,status,created_at),
    KEY idx_workspace_invitations_email (email,status,expires_at),
    CONSTRAINT fk_workspace_invitations_workspace FOREIGN KEY (workspace_id) REFERENCES workspaces(id),
    CONSTRAINT fk_workspace_invitations_inviter FOREIGN KEY (invited_by) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
