-- A record's anchor timestamp is not proof that symptom onset was reported.
ALTER TABLE episodes ADD COLUMN onset_known boolean NOT NULL DEFAULT false;
