-- ADR 0033 follow-up: persist CI security-gate outcomes so the admin
-- UI can render current posture without re-querying GitHub Actions
-- on every request.
--
-- One row per completed CI run, per scan type. The admin console
-- reads the latest row per scan_type via
-- /api/v1/platform/security/posture (aggregated in the workflow
-- service's platform handler).
--
-- Platform-scoped table — NOT tenant-scoped. CI posture is a property
-- of the repo, not a tenant. No RLS.

CREATE TABLE IF NOT EXISTS security_scans (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    -- Matches the four ADR 0033 gates. CHECK keeps rogue writers
    -- (e.g. a new CI job) from inserting types the UI doesn't render.
    scan_type       TEXT NOT NULL CHECK (scan_type IN ('sast', 'dep_scan', 'dast', 'secret_scan')),
    status          TEXT NOT NULL CHECK (status IN ('pass', 'fail')),

    -- Severity counters. Tools report different granularities; the UI
    -- only shows critical+high, but we store the full breakdown for
    -- the Security tab drill-down.
    critical_count  INT  NOT NULL DEFAULT 0 CHECK (critical_count >= 0),
    high_count      INT  NOT NULL DEFAULT 0 CHECK (high_count     >= 0),
    medium_count    INT  NOT NULL DEFAULT 0 CHECK (medium_count   >= 0),
    low_count       INT  NOT NULL DEFAULT 0 CHECK (low_count      >= 0),

    -- CI run metadata so the UI can deep-link to the run log.
    -- run_id is GH Actions' numeric run id; run_url is the full URL
    -- (captured verbatim from $GITHUB_SERVER_URL + $GITHUB_REPOSITORY
    -- + /actions/runs/$RUN_ID in the workflow).
    run_id          TEXT,
    run_url         TEXT,
    commit_sha      TEXT,
    branch          TEXT,

    ran_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Hot path: SELECT ... WHERE scan_type = $1 ORDER BY ran_at DESC LIMIT 1.
-- One row per read per scan type; index is ~trivially small.
CREATE INDEX IF NOT EXISTS idx_security_scans_type_ran_at
    ON security_scans (scan_type, ran_at DESC);

COMMENT ON TABLE security_scans IS 'CI security-gate outcomes (ADR 0033). Read by /api/v1/platform/security/posture for the admin UI.';
