CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE users (
  id uuid PRIMARY KEY, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE profiles (
  user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  data jsonb NOT NULL DEFAULT '{}', version integer NOT NULL DEFAULT 1, updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE conversations (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), user_id uuid NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE attachments (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  filename text NOT NULL, content_type text NOT NULL, size_bytes bigint NOT NULL, sha256 text NOT NULL,
  width integer NOT NULL, height integer NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE messages (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), conversation_id uuid NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE, role text NOT NULL CHECK(role IN ('user','assistant')),
  content text NOT NULL, attachments jsonb NOT NULL DEFAULT '[]', metadata jsonb NOT NULL DEFAULT '{}',
  client_id uuid, created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(user_id,client_id)
);
CREATE INDEX messages_conversation_idx ON messages(conversation_id,created_at,id);
CREATE TABLE episodes (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  topic text NOT NULL DEFAULT '其他不适', complaint text NOT NULL,
  started_at timestamptz NOT NULL, status text NOT NULL DEFAULT 'active' CHECK(status IN ('active','archived')),
  version integer NOT NULL DEFAULT 1, archive_reason text, source_message_id uuid REFERENCES messages(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX episodes_owner_idx ON episodes(user_id,status,started_at);
CREATE TABLE observations (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), episode_id uuid NOT NULL REFERENCES episodes(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE, observed_date date NOT NULL,
  data jsonb NOT NULL, version integer NOT NULL DEFAULT 1, source_message_id uuid REFERENCES messages(id) ON DELETE SET NULL,
  recorded_at timestamptz NOT NULL DEFAULT now(), UNIQUE(episode_id,observed_date)
);
CREATE TABLE observation_revisions (
  id bigserial PRIMARY KEY, observation_id uuid NOT NULL REFERENCES observations(id) ON DELETE CASCADE,
  data jsonb NOT NULL, version integer NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE medication_uses (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  episode_id uuid REFERENCES episodes(id) ON DELETE CASCADE, data jsonb NOT NULL,
  source_message_id uuid REFERENCES messages(id) ON DELETE SET NULL, version integer NOT NULL DEFAULT 1,
  recorded_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX medication_owner_idx ON medication_uses(user_id,recorded_at);
CREATE TABLE care_tasks (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  episode_id uuid REFERENCES episodes(id) ON DELETE CASCADE, title text NOT NULL,
  due_at timestamptz NOT NULL, recurrence text NOT NULL DEFAULT 'once' CHECK(recurrence IN ('once','daily')),
  status text NOT NULL DEFAULT 'scheduled' CHECK(status IN ('scheduled','due','completed','paused')),
  version integer NOT NULL DEFAULT 1, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX care_tasks_due_idx ON care_tasks(status,due_at);
CREATE TABLE agent_runs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  conversation_id uuid NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  message_id uuid NOT NULL UNIQUE REFERENCES messages(id) ON DELETE CASCADE,
  status text NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','running','waiting_user','succeeded','failed','cancelled')),
  attempts integer NOT NULL DEFAULT 0, lease_until timestamptz, error_code text,
  provider text, version integer NOT NULL DEFAULT 1, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX agent_runs_jobs_idx ON agent_runs(status,created_at);
CREATE TABLE run_events (
  id bigserial PRIMARY KEY, run_id uuid NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
  type text NOT NULL, data jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX run_events_run_idx ON run_events(run_id,id);
CREATE TABLE actions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  run_id uuid NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE, kind text NOT NULL, payload jsonb NOT NULL,
  expected_version integer, status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','confirmed','rejected','expired')),
  result jsonb, expires_at timestamptz NOT NULL DEFAULT now()+interval '24 hours', created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(run_id,kind)
);
CREATE TABLE knowledge_items (
  id text PRIMARY KEY, topic text NOT NULL, title text NOT NULL, content text NOT NULL,
  source_url text NOT NULL, version text NOT NULL, region text NOT NULL DEFAULT 'general',
  review_status text NOT NULL DEFAULT 'reference' CHECK(review_status IN ('reference','reviewed','retired')),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE audit_events (
  id bigserial PRIMARY KEY, actor_id uuid REFERENCES users(id) ON DELETE SET NULL, action text NOT NULL,
  resource_id text NOT NULL, purpose text NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE deletion_jobs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), user_id uuid NOT NULL, conversation_id uuid NOT NULL,
  status text NOT NULL DEFAULT 'queued', created_at timestamptz NOT NULL DEFAULT now()
);

GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO hc_app;
GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO hc_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT,INSERT,UPDATE,DELETE ON TABLES TO hc_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE,SELECT ON SEQUENCES TO hc_app;
