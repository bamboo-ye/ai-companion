CREATE TABLE medication_revisions (
 id bigserial PRIMARY KEY,
 medication_id uuid NOT NULL REFERENCES medication_uses(id) ON DELETE CASCADE,
 data jsonb NOT NULL, version integer NOT NULL,
 recorded_at timestamptz NOT NULL DEFAULT now()
);
