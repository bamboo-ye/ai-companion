ALTER TABLE deletion_jobs ADD COLUMN attachment_ids jsonb NOT NULL DEFAULT '[]';
CREATE TABLE action_receipts (
  action_id uuid PRIMARY KEY, user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  result jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX action_receipts_user_idx ON action_receipts(user_id);
