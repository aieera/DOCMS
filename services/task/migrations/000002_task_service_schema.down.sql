-- Down migration for 000002_task_service_schema.up.sql: drop the four
-- side tables (reverse creation order so FKs onto tasks don't matter
-- either way) and undo the tasks.deleted_at adoption.
DROP TABLE IF EXISTS task_activity;
DROP TABLE IF EXISTS task_comments;
DROP TABLE IF EXISTS task_documents;
DROP TABLE IF EXISTS task_assignees;

ALTER TABLE tasks DROP COLUMN IF EXISTS deleted_at;
