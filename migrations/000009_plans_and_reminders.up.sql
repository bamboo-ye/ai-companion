CREATE TABLE plans (
    id BINARY(16) NOT NULL,
    user_id BINARY(16) NOT NULL,
    title VARCHAR(160) NOT NULL,
    local_date DATE NOT NULL,
    timezone VARCHAR(64) NOT NULL,
    status ENUM('active','completed','cancelled') NOT NULL DEFAULT 'active',
    created_at TIMESTAMP(6) NOT NULL,
    updated_at TIMESTAMP(6) NOT NULL,
    PRIMARY KEY (id),
    KEY idx_plans_user_date (user_id,status,local_date),
    CONSTRAINT fk_plans_user FOREIGN KEY (user_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE plan_items (
    id BINARY(16) NOT NULL,
    plan_id BINARY(16) NOT NULL,
    title VARCHAR(255) NOT NULL,
    priority ENUM('low','medium','high') NOT NULL,
    estimated_minutes SMALLINT UNSIGNED NOT NULL DEFAULT 0,
    starts_at TIMESTAMP(6) NULL,
    ends_at TIMESTAMP(6) NULL,
    location VARCHAR(255) NOT NULL DEFAULT '',
    status ENUM('pending','in_progress','completed','skipped','cancelled') NOT NULL DEFAULT 'pending',
    source VARCHAR(64) NOT NULL DEFAULT '',
    created_at TIMESTAMP(6) NOT NULL,
    updated_at TIMESTAMP(6) NOT NULL,
    PRIMARY KEY (id),
    KEY idx_plan_items_plan_status (plan_id,status,starts_at),
    CONSTRAINT fk_plan_items_plan FOREIGN KEY (plan_id) REFERENCES plans(id),
    CONSTRAINT chk_plan_items_range CHECK (ends_at IS NULL OR starts_at IS NULL OR ends_at > starts_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE reminders (
    id BINARY(16) NOT NULL,
    user_id BINARY(16) NOT NULL,
    plan_item_id BINARY(16) NULL,
    source_message_id BINARY(16) NULL,
    raw_text VARCHAR(2000) NOT NULL,
    title VARCHAR(255) NOT NULL,
    due_at TIMESTAMP(6) NULL,
    local_due VARCHAR(32) NOT NULL DEFAULT '',
    timezone VARCHAR(64) NOT NULL,
    time_precision ENUM('minute','part_of_day','date') NULL,
    recurrence ENUM('none','daily','weekly') NOT NULL DEFAULT 'none',
    needs_clarification JSON NOT NULL,
    status ENUM('needs_clarification','pending_confirmation','active','completed','cancelled') NOT NULL,
    confirmation_key VARCHAR(191) NULL,
    system_sync_status ENUM('not_requested','pending','synced','permission_denied','failed','conflict','disconnected') NOT NULL DEFAULT 'not_requested',
    created_at TIMESTAMP(6) NOT NULL,
    updated_at TIMESTAMP(6) NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_reminders_user_confirmation (user_id,confirmation_key),
    KEY idx_reminders_user_due (user_id,status,due_at),
    CONSTRAINT fk_reminders_user FOREIGN KEY (user_id) REFERENCES users(id),
    CONSTRAINT fk_reminders_plan_item FOREIGN KEY (plan_item_id) REFERENCES plan_items(id),
    CONSTRAINT fk_reminders_message FOREIGN KEY (source_message_id) REFERENCES messages(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE external_reminder_links (
    reminder_id BINARY(16) NOT NULL,
    provider ENUM('ios_eventkit','android_calendar','android_alarm','google_tasks') NOT NULL,
    external_id VARCHAR(512) NOT NULL,
    external_revision VARCHAR(255) NOT NULL DEFAULT '',
    sync_status ENUM('synced','permission_denied','failed','conflict','disconnected') NOT NULL,
    last_error_code VARCHAR(128) NOT NULL DEFAULT '',
    created_at TIMESTAMP(6) NOT NULL,
    updated_at TIMESTAMP(6) NOT NULL,
    PRIMARY KEY (reminder_id),
    UNIQUE KEY uk_external_reminder_provider_id (provider,external_id),
    CONSTRAINT fk_external_reminder_links_reminder FOREIGN KEY (reminder_id) REFERENCES reminders(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE reminder_events (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    reminder_id BINARY(16) NOT NULL,
    user_id BINARY(16) NOT NULL,
    event_type ENUM('confirmed','completed','snoozed','skipped','rescheduled','sync_updated') NOT NULL,
    event_data JSON NOT NULL,
    occurred_at TIMESTAMP(6) NOT NULL,
    PRIMARY KEY (id),
    KEY idx_reminder_events_user (user_id,occurred_at),
    CONSTRAINT fk_reminder_events_reminder FOREIGN KEY (reminder_id) REFERENCES reminders(id),
    CONSTRAINT fk_reminder_events_user FOREIGN KEY (user_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE notification_deliveries (
    id BINARY(16) NOT NULL,
    reminder_id BINARY(16) NOT NULL,
    user_id BINARY(16) NOT NULL,
    channel ENUM('in_app','apns','fcm','local') NOT NULL,
    scheduled_at TIMESTAMP(6) NOT NULL,
    status ENUM('queued','delivered','failed','cancelled') NOT NULL,
    provider_message_id VARCHAR(512) NULL,
    failure_code VARCHAR(128) NULL,
    created_at TIMESTAMP(6) NOT NULL,
    updated_at TIMESTAMP(6) NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_notification_delivery_once (reminder_id,channel,scheduled_at),
    KEY idx_notification_deliveries_due (status,scheduled_at),
    CONSTRAINT fk_notification_deliveries_reminder FOREIGN KEY (reminder_id) REFERENCES reminders(id),
    CONSTRAINT fk_notification_deliveries_user FOREIGN KEY (user_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
