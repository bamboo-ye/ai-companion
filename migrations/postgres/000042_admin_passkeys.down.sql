DROP TABLE IF EXISTS ops.operator_passkey_records;
ALTER TABLE app.operator_accounts DROP COLUMN session_version;
