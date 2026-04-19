BEGIN;

DROP INDEX IF EXISTS idx_wf_tasks_assignee_pending;

ALTER TABLE workflow_tasks
    DROP CONSTRAINT IF EXISTS workflow_tasks_document_fkey;

ALTER TABLE workflow_tasks
    DROP COLUMN IF EXISTS document_id;

-- Note: we do NOT restore NOT NULL on step_id; any task rows inserted
-- by the workflow service between 000005 up and down would have NULL
-- step_id and the NOT NULL would fail. Rollback is tolerant.

COMMIT;
