DROP TABLE IF EXISTS app.operator_accounts;
DROP TABLE IF EXISTS app.user_safety_policies;
DROP TABLE IF EXISTS app.billing_subscriptions;
DROP TABLE IF EXISTS app.billing_plans;
DROP TABLE IF EXISTS app.email_deliveries;

ALTER TABLE app.workspace_generated_file_shares
    DROP CONSTRAINT IF EXISTS workspace_generated_file_shares_workspace_fk;

ALTER TABLE app.workspace_document_shares
    DROP CONSTRAINT IF EXISTS workspace_document_shares_workspace_fk;

ALTER TABLE app.workspace_ledger_export_shares
    DROP CONSTRAINT IF EXISTS workspace_ledger_export_shares_workspace_fk;

DROP TABLE IF EXISTS app.workspace_invitations;
DROP TABLE IF EXISTS app.workspace_members;
DROP TABLE IF EXISTS app.workspaces;
