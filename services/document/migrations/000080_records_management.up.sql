-- Records management (DoD-5015.2-style): a file plan of categories/series,
-- retention schedules attached to plan nodes, and record declarations that
-- freeze a document until a certified disposition.
--
-- Lifecycle (CLAUDE.md): a declared record's document is immutable
-- (edit/delete blocked in the document write paths) until disposition, which
-- transitions it to `disposed` and writes a tamper-evident audit event via
-- dms.record.disposed.v1 (outbox → audit hash chain).

-- ---------------------------------------------------------------------------
-- retention_schedules — trigger + retention period + disposition action.
-- Attached to a file-plan node; the declared record snapshots the resolved
-- schedule so later edits to the schedule don't retroactively move cutoffs.
-- ---------------------------------------------------------------------------
CREATE TABLE retention_schedules (
    tenant_id             UUID NOT NULL REFERENCES organizations(id),
    id                    UUID NOT NULL DEFAULT gen_random_uuid(),
    name                  TEXT NOT NULL,
    description           TEXT,
    -- what starts the retention clock
    trigger_event         TEXT NOT NULL DEFAULT 'declaration'
        CHECK (trigger_event IN ('declaration', 'creation', 'event', 'superseded', 'fixed_date')),
    retention_period_days INTEGER NOT NULL DEFAULT 0 CHECK (retention_period_days >= 0),
    -- what happens at cutoff
    disposition_action    TEXT NOT NULL DEFAULT 'review'
        CHECK (disposition_action IN ('destroy', 'transfer', 'permanent', 'review')),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id)
);
ALTER TABLE retention_schedules ENABLE ROW LEVEL SECURITY;
ALTER TABLE retention_schedules FORCE  ROW LEVEL SECURITY;
CREATE POLICY retention_schedules_tenant_isolation ON retention_schedules
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY retention_schedules_tenant_isolation_insert ON retention_schedules
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ---------------------------------------------------------------------------
-- record_categories — the file-plan tree. parent_id NULL = root. node_type
-- 'series' nodes are the leaves documents get declared against (and the
-- usual carrier of a retention schedule); 'category' nodes are branches.
-- ---------------------------------------------------------------------------
CREATE TABLE record_categories (
    tenant_id             UUID NOT NULL REFERENCES organizations(id),
    id                    UUID NOT NULL DEFAULT gen_random_uuid(),
    parent_id             UUID,
    name                  TEXT NOT NULL,
    code                  TEXT,                  -- optional file-plan code, e.g. "1100.2"
    node_type             TEXT NOT NULL DEFAULT 'category'
        CHECK (node_type IN ('category', 'series')),
    description           TEXT,
    retention_schedule_id UUID,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, parent_id)             REFERENCES record_categories(tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, retention_schedule_id) REFERENCES retention_schedules(tenant_id, id)
);
CREATE INDEX idx_record_categories_parent ON record_categories(tenant_id, parent_id);
ALTER TABLE record_categories ENABLE ROW LEVEL SECURITY;
ALTER TABLE record_categories FORCE  ROW LEVEL SECURITY;
CREATE POLICY record_categories_tenant_isolation ON record_categories
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY record_categories_tenant_isolation_insert ON record_categories
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ---------------------------------------------------------------------------
-- records — one declaration linking a document to a file-plan node. A
-- document is one record (UNIQUE document_id). disposition_state drives the
-- immutability gate and the disposition review queue.
-- ---------------------------------------------------------------------------
CREATE TABLE records (
    tenant_id             UUID NOT NULL REFERENCES organizations(id),
    id                    UUID NOT NULL DEFAULT gen_random_uuid(),
    document_id           UUID NOT NULL,
    category_id           UUID NOT NULL,
    -- schedule resolved + snapshotted at declaration time
    retention_schedule_id UUID,
    disposition_action    TEXT,                  -- snapshot of the schedule's action
    disposition_state     TEXT NOT NULL DEFAULT 'declared'
        CHECK (disposition_state IN ('declared', 'cutoff_pending', 'disposed', 'transferred')),
    declared_by           UUID,
    declared_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- when retention expires; NULL until a trigger resolves it
    cutoff_date           TIMESTAMPTZ,
    disposed_by           UUID,
    disposed_at           TIMESTAMPTZ,
    -- the audit_events id of the tamper-evident disposition certificate
    disposition_event_id  UUID,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, document_id),
    FOREIGN KEY (tenant_id, document_id)           REFERENCES documents(tenant_id, id),
    FOREIGN KEY (tenant_id, category_id)           REFERENCES record_categories(tenant_id, id),
    FOREIGN KEY (tenant_id, retention_schedule_id) REFERENCES retention_schedules(tenant_id, id)
);
-- Hot path: the immutability gate looks records up by document_id; the
-- disposition queue scans by (state, cutoff_date).
CREATE INDEX idx_records_document ON records(tenant_id, document_id);
CREATE INDEX idx_records_cutoff   ON records(tenant_id, disposition_state, cutoff_date);
ALTER TABLE records ENABLE ROW LEVEL SECURITY;
ALTER TABLE records FORCE  ROW LEVEL SECURITY;
CREATE POLICY records_tenant_isolation ON records
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY records_tenant_isolation_insert ON records
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
