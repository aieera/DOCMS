-- Reverse-order drop of the initial schema. Tables are dropped in the
-- opposite order they were created so composite FKs never fire.

BEGIN;

DROP TABLE IF EXISTS sso_configs;
DROP TABLE IF EXISTS conversation_history;
DROP TABLE IF EXISTS tenant_metadata_schemas;
DROP TABLE IF EXISTS outbox;
DROP TABLE IF EXISTS usage_meters;
DROP TABLE IF EXISTS subscriptions_billing;
DROP TABLE IF EXISTS connector_configs;
DROP TABLE IF EXISTS webhook_deliveries;
DROP TABLE IF EXISTS webhook_subscriptions;
DROP TABLE IF EXISTS signature_signers;
DROP TABLE IF EXISTS signature_requests;
DROP TABLE IF EXISTS device_tokens;
DROP TABLE IF EXISTS notifications;
DROP TABLE IF EXISTS notification_preferences;
DROP TABLE IF EXISTS workflow_tasks;
DROP TABLE IF EXISTS workflow_instances;
DROP TABLE IF EXISTS workflow_definitions;
DROP TABLE IF EXISTS duplicate_candidates;
DROP TABLE IF EXISTS document_fingerprints;
DROP TABLE IF EXISTS entities;
DROP TABLE IF EXISTS document_chunks;
DROP TABLE IF EXISTS extraction_results;
DROP TABLE IF EXISTS ocr_results;
DROP TABLE IF EXISTS upload_sessions;
DROP TABLE IF EXISTS api_keys;
DROP TABLE IF EXISTS sessions;

-- audit_events is partitioned; dropping the parent cascades to partitions.
DROP TABLE IF EXISTS audit_events;

DROP TABLE IF EXISTS retention_policies;
DROP TABLE IF EXISTS legal_hold_documents;
DROP TABLE IF EXISTS legal_holds;
DROP TABLE IF EXISTS tags_catalog;
DROP TABLE IF EXISTS annotations;
DROP TABLE IF EXISTS comments;
DROP TABLE IF EXISTS share_links;
DROP TABLE IF EXISTS permissions;

-- documents.current_version_id → versions(tenant_id, id); drop the FK first.
ALTER TABLE IF EXISTS documents DROP CONSTRAINT IF EXISTS documents_current_version_fk;
DROP TABLE IF EXISTS versions;
DROP TABLE IF EXISTS content_blobs;
DROP TABLE IF EXISTS documents;
DROP TABLE IF EXISTS folders;
DROP TABLE IF EXISTS workspace_members;
DROP TABLE IF EXISTS workspaces;
DROP TABLE IF EXISTS group_members;
DROP TABLE IF EXISTS groups;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS organizations;

DROP FUNCTION IF EXISTS update_updated_at_column();

COMMIT;
