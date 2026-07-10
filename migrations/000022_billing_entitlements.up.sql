CREATE TABLE billing_plans (
    code VARCHAR(64) NOT NULL,
    display_name VARCHAR(120) NOT NULL,
    status ENUM('active','archived') NOT NULL DEFAULT 'active',
    document_limit INT NOT NULL,
    skill_runs_monthly_limit INT NOT NULL,
    workspace_limit INT NOT NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (code)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

INSERT INTO billing_plans (code,display_name,status,document_limit,skill_runs_monthly_limit,workspace_limit)
VALUES
    ('free','Free','active',10,20,3),
    ('pro','Pro','active',200,1000,20),
    ('team','Team','active',1000,5000,100)
ON DUPLICATE KEY UPDATE
    display_name=VALUES(display_name),
    status=VALUES(status),
    document_limit=VALUES(document_limit),
    skill_runs_monthly_limit=VALUES(skill_runs_monthly_limit),
    workspace_limit=VALUES(workspace_limit);

CREATE TABLE billing_subscriptions (
    id BINARY(16) NOT NULL,
    user_id BINARY(16) NOT NULL,
    plan_code VARCHAR(64) NOT NULL,
    status ENUM('trialing','active','past_due','cancelled','expired') NOT NULL DEFAULT 'active',
    current_period_start TIMESTAMP(6) NOT NULL,
    current_period_end TIMESTAMP(6) NOT NULL,
    provider VARCHAR(64) NOT NULL DEFAULT '',
    provider_ref VARCHAR(191) NOT NULL DEFAULT '',
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    KEY idx_billing_subscriptions_user_status_period (user_id,status,current_period_end),
    KEY idx_billing_subscriptions_provider_ref (provider,provider_ref),
    CONSTRAINT fk_billing_subscriptions_user FOREIGN KEY (user_id) REFERENCES users(id),
    CONSTRAINT fk_billing_subscriptions_plan FOREIGN KEY (plan_code) REFERENCES billing_plans(code)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
