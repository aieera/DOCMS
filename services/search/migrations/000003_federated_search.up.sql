-- ADR 0069 — platform-admin federated search.
--
-- Two new platform-level tables (NOT tenant-scoped):
--   platform_admins         — allow-list for the federated path
--   federated_search_audit  — append-only ledger of every cross-tenant query

CREATE TABLE IF NOT EXISTS platform_admins (
    user_id     UUID         PRIMARY KEY,
    granted_by  UUID         NOT NULL,
    granted_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    -- Required reason for the grant — surfaces in the privacy /
    -- compliance audit when reviewing who has cross-tenant read.
    reason      TEXT         NOT NULL,
    revoked_at  TIMESTAMPTZ
);

-- Lookup index used on every federated request — must be O(1).
CREATE INDEX IF NOT EXISTS idx_platform_admins_active
    ON platform_admins (user_id)
    WHERE revoked_at IS NULL;

-- Deliberately NO row level security: the platform_admins table is
-- the source of truth for who can BYPASS tenant isolation. Putting
-- it under tenant RLS would be circular. The handlers that read it
-- run with no tenant context.


CREATE TABLE IF NOT EXISTS federated_search_audit (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    caller_id       UUID        NOT NULL,
    -- Reason field is REQUIRED on every query (validated at the
    -- handler). This is the single load-bearing audit field — every
    -- compliance reviewer's first question is "why was this run".
    reason          TEXT        NOT NULL,
    -- Full query payload as submitted, snapshot. Includes filter
    -- state so a reviewer can reproduce the call. PII expectations:
    -- a query body may contain a docpath / search term that is
    -- itself sensitive; the table is access-controlled by platform
    -- admin only and the retention policy is "forever, never delete".
    query_payload   JSONB       NOT NULL,
    -- Top-line summary: total hits + per-tenant breakdown. Full
    -- result-set is NOT stored (size + privacy); the auditor can
    -- re-run the exact query if a deeper inspection is needed.
    results_summary JSONB       NOT NULL DEFAULT '{}'::jsonb,
    latency_ms      INTEGER     NOT NULL DEFAULT 0,
    -- Outcome — distinguishes "rate-limit denied" / "perm denied" /
    -- "ran". Always recorded so denied attempts are visible too.
    outcome         TEXT        NOT NULL DEFAULT 'success'
                                 CHECK (outcome IN ('success','denied_perm','denied_quota','error')),
    error_kind      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Per-admin queries-per-day counter (the rate-limit window) reads
-- this index.
CREATE INDEX IF NOT EXISTS idx_federated_audit_caller_day
    ON federated_search_audit (caller_id, created_at DESC);

-- For the admin's own UI showing recent queries.
CREATE INDEX IF NOT EXISTS idx_federated_audit_recent
    ON federated_search_audit (created_at DESC);
