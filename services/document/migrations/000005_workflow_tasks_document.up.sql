-- Wave 7 Prompt 7.4: align `workflow_tasks` with the activity/repository code.
--
-- The initial schema (000001) created `workflow_tasks` with a required
-- `step_id TEXT NOT NULL` column and no `document_id` column. The
-- workflow service, however, never writes `step_id` and always writes a
-- `document_id` (see services/workflow/internal/activities/activities.go
-- `CreateTask`). In Wave 7 Prompt 7.1 the workflow scaffold graduated
-- from stub to in-use; the inbox read path (this prompt) surfaces the
-- mismatch, so we fix it here rather than papering over it in the query.
--
-- Changes:
--   1. ADD document_id UUID, FK to documents(tenant_id, id). Nullable —
--      system-initiated tasks (e.g. retention cron) may not be bound to
--      a specific document.
--   2. DROP NOT NULL on step_id. Current activity code passes step_name
--      only; step_id is reserved for future per-step identifiers from
--      the ReactFlow designer (Wave 7 Prompt 7.5).
--   3. ADD a partial index on (tenant_id, assignee_id, status) for the
--      hot inbox query `WHERE tenant_id = ? AND assignee_id = ?
--      AND status = 'pending'`. The existing idx_wf_tasks_assignee
--      covers this but isn't partial; the new one is smaller and kept
--      fresh on pending writes only.
--
-- No backfill: the existing table is empty in all environments the
-- document service has deployed to (workflow was stubbed before
-- Wave 7).
BEGIN;

ALTER TABLE workflow_tasks
    ADD COLUMN IF NOT EXISTS document_id UUID;

ALTER TABLE workflow_tasks
    ALTER COLUMN step_id DROP NOT NULL;

-- FK is composite (tenant_id, document_id) to play nicely with the
-- tenant-scoped composite PK on documents.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'workflow_tasks_document_fkey'
    ) THEN
        ALTER TABLE workflow_tasks
            ADD CONSTRAINT workflow_tasks_document_fkey
            FOREIGN KEY (tenant_id, document_id)
            REFERENCES documents(tenant_id, id)
            ON DELETE SET NULL;
    END IF;
END$$;

CREATE INDEX IF NOT EXISTS idx_wf_tasks_assignee_pending
    ON workflow_tasks(tenant_id, assignee_id, created_at DESC)
    WHERE status = 'pending';

COMMIT;
