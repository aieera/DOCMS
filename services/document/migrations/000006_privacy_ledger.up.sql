-- Wave 8 Prompt 8.3: GDPR data-subject request (DSR) ledger + request
-- tracking.
--
-- Two tables:
--
--   privacy_dsr_requests — operational row per in-flight workflow.
--     Clients poll GET /privacy/dsr/{id} for status. Rows may be
--     pruned 30 days after `completed_at` (operator-driven; no cron
--     yet — logged out-of-scope).
--
--   privacy_ledger — permanent audit log. 7-year retention (per
--     `DMS Architecture/final.md` §7.3). Never delete rows; redact
--     payloads in export instead. Enforcement of the retention clock
--     lives outside this migration (compliance SoP document).
--
-- Index plan:
--   privacy_dsr_requests:
--     PK (tenant_id, id) — hot row lookup.
--     idx on (tenant_id, status, created_at DESC) — admin list page.
--   privacy_ledger:
--     PK (id) — append-only; tenant is a payload attribute.
--     idx on (tenant_id, subject_email, created_at DESC) — investigation query.
--     idx on (created_at) — retention sweep.
BEGIN;

CREATE TABLE privacy_dsr_requests (
    tenant_id         UUID NOT NULL REFERENCES organizations(id),
    id                UUID NOT NULL DEFAULT gen_random_uuid(),
    request_type      TEXT NOT NULL CHECK (request_type IN ('export','erase','anonymize')),
    subject_email     TEXT NOT NULL,
    requested_by      UUID,
    status            TEXT NOT NULL DEFAULT 'pending'
                          CHECK (status IN ('pending','running','completed','blocked','failed')),
    workflow_run_id   TEXT,
    export_url        TEXT,
    export_url_expires_at TIMESTAMPTZ,
    result_summary    JSONB,
    blocked_reason    TEXT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at      TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, requested_by) REFERENCES users(tenant_id, id)
);
CREATE INDEX idx_dsr_tenant_status
    ON privacy_dsr_requests(tenant_id, status, created_at DESC);

ALTER TABLE privacy_dsr_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE privacy_dsr_requests FORCE  ROW LEVEL SECURITY;
CREATE POLICY dsr_tenant_isolation ON privacy_dsr_requests
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY dsr_tenant_isolation_insert ON privacy_dsr_requests
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TABLE privacy_ledger (
    id             UUID NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
    tenant_id      UUID NOT NULL REFERENCES organizations(id),
    request_id     UUID,
    subject_email  TEXT NOT NULL,
    action         TEXT NOT NULL CHECK (action IN ('export','erase','anonymize','hold_block','verify','error')),
    outcome        TEXT NOT NULL,
    details        JSONB NOT NULL DEFAULT '{}'::jsonb,
    actor_id       UUID,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_ledger_tenant_subject
    ON privacy_ledger(tenant_id, subject_email, created_at DESC);
CREATE INDEX idx_ledger_created_at
    ON privacy_ledger(created_at);

-- privacy_ledger is intentionally NOT RLS-wrapped at the table level:
-- the compliance officer role reads across the ledger by policy.
-- Application-level tenant filtering is still applied. Rationale lives
-- in ADR 0024.

COMMIT;
