-- Task service adopts the ADR-0068 tasks table (created by document
-- migration 000033) and adds the relational side tables from the
-- 2026-07-28 task-service design. Same shared database; ownership of
-- these tables transfers to the task service from this migration on.

ALTER TABLE tasks ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;

CREATE TABLE task_assignees (
    tenant_id UUID NOT NULL,
    task_id   UUID NOT NULL,
    user_id   UUID NOT NULL,
    added_by  UUID NOT NULL,
    added_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, task_id, user_id),
    FOREIGN KEY (tenant_id, task_id) REFERENCES tasks (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_task_assignees_user ON task_assignees (tenant_id, user_id);

CREATE TABLE task_documents (
    tenant_id      UUID NOT NULL,
    task_id        UUID NOT NULL,
    document_id    UUID NOT NULL,
    workspace_id   UUID NOT NULL,
    title_snapshot TEXT NOT NULL DEFAULT '',
    linked_by      UUID NOT NULL,
    linked_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, task_id, document_id),
    FOREIGN KEY (tenant_id, task_id) REFERENCES tasks (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_task_documents_document ON task_documents (tenant_id, document_id);

CREATE TABLE task_comments (
    tenant_id  UUID NOT NULL,
    id         UUID NOT NULL DEFAULT gen_random_uuid(),
    task_id    UUID NOT NULL,
    author_id  UUID NOT NULL,
    body       TEXT NOT NULL CHECK (char_length(body) <= 4000),
    mentions   UUID[] NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, task_id) REFERENCES tasks (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_task_comments_task ON task_comments (tenant_id, task_id, created_at);

CREATE TABLE task_activity (
    tenant_id  UUID NOT NULL,
    id         BIGINT GENERATED ALWAYS AS IDENTITY,
    task_id    UUID NOT NULL,
    actor_id   UUID NOT NULL,
    action     TEXT NOT NULL CHECK (action IN (
        'created','updated','assigned','unassigned','status_changed',
        'document_linked','document_unlinked','commented','deleted')),
    detail     JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, task_id) REFERENCES tasks (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_task_activity_task ON task_activity (tenant_id, task_id, id DESC);

-- RLS: identical posture to every other tenant table.
ALTER TABLE task_assignees ENABLE ROW LEVEL SECURITY;
ALTER TABLE task_assignees FORCE ROW LEVEL SECURITY;
CREATE POLICY task_assignees_tenant_isolation ON task_assignees
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
ALTER TABLE task_documents ENABLE ROW LEVEL SECURITY;
ALTER TABLE task_documents FORCE ROW LEVEL SECURITY;
CREATE POLICY task_documents_tenant_isolation ON task_documents
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
ALTER TABLE task_comments ENABLE ROW LEVEL SECURITY;
ALTER TABLE task_comments FORCE ROW LEVEL SECURITY;
CREATE POLICY task_comments_tenant_isolation ON task_comments
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
ALTER TABLE task_activity ENABLE ROW LEVEL SECURITY;
ALTER TABLE task_activity FORCE ROW LEVEL SECURITY;
CREATE POLICY task_activity_tenant_isolation ON task_activity
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- Backfill: legacy single assignee -> task_assignees; legacy linked
-- document -> task_documents (title/workspace snapshotted via JOIN).
INSERT INTO task_assignees (tenant_id, task_id, user_id, added_by, added_at)
SELECT t.tenant_id, t.id, t.assignee_id, t.created_by, t.created_at
  FROM tasks t
 WHERE t.assignee_id IS NOT NULL
ON CONFLICT DO NOTHING;

INSERT INTO task_documents (tenant_id, task_id, document_id, workspace_id, title_snapshot, linked_by, linked_at)
SELECT t.tenant_id, t.id, t.linked_document_id, d.workspace_id, d.title, t.created_by, t.created_at
  FROM tasks t
  JOIN documents d ON d.tenant_id = t.tenant_id AND d.id = t.linked_document_id
 WHERE t.linked_document_id IS NOT NULL
ON CONFLICT DO NOTHING;
