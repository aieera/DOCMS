-- ADR 0062 — LDAP / AD direct bind.
--
-- Three new tables:
--   ldap_configs          — per-tenant directory settings + sealed bind pw
--   ldap_group_mappings   — AD group DN → DMS group id
--   ldap_sync_history     — audit/debugging trail for sync runs

CREATE TABLE IF NOT EXISTS ldap_configs (
    tenant_id              UUID         NOT NULL REFERENCES organizations(id),
    id                     UUID         NOT NULL DEFAULT gen_random_uuid(),
    -- Connection. Scheme is part of the URL: ldap://, ldaps://.
    url                    TEXT         NOT NULL,
    -- STARTTLS upgrade after plain bind. Ignored for ldaps://.
    use_starttls           BOOLEAN      NOT NULL DEFAULT TRUE,
    -- When false (default) plain ldap:// without STARTTLS is rejected;
    -- the form surfaces this as a red opt-in.
    allow_insecure         BOOLEAN      NOT NULL DEFAULT FALSE,
    -- Service account for searches. The user-bind for login uses the
    -- supplied credentials, not this account.
    bind_dn                TEXT         NOT NULL,
    -- Envelope-encrypted ciphertext from pkg/crypto/envelope. Holds
    -- the sealed bind password — never the plaintext. Wrapped by the
    -- per-tenant KEK so KEK rotation rotates this too.
    bind_password_sealed   BYTEA        NOT NULL,
    -- User search.
    user_search_base       TEXT         NOT NULL,
    -- {username} is substituted at runtime. Defaults: AD ->
    -- (sAMAccountName={username}), OpenLDAP -> (uid={username}).
    user_search_filter     TEXT         NOT NULL,
    -- Email attribute on the user entry. Defaults: AD -> mail,
    -- OpenLDAP -> mail.
    email_attribute        TEXT         NOT NULL DEFAULT 'mail',
    display_name_attribute TEXT         NOT NULL DEFAULT 'displayName',
    -- Group search.
    group_search_base      TEXT         NOT NULL,
    -- {user_dn} is substituted with the bound user's DN. Defaults:
    -- AD/OpenLDAP -> (member={user_dn}).
    group_search_filter    TEXT         NOT NULL,
    -- Resolve nested group memberships transitively. AD uses the
    -- LDAP_MATCHING_RULE_IN_CHAIN OID; OpenLDAP falls back to
    -- recursive resolution with a depth cap.
    nested_groups          BOOLEAN      NOT NULL DEFAULT TRUE,
    -- When AD bind fails with INVALID_CREDENTIALS, fall through to
    -- local password auth. Default off — admins flip this on
    -- temporarily during a cutover.
    fallback_to_local      BOOLEAN      NOT NULL DEFAULT FALSE,
    -- When false the login flow ignores LDAP entirely.
    is_active              BOOLEAN      NOT NULL DEFAULT FALSE,
    -- Last successful sync — surfaced in the admin UI.
    last_sync_at           TIMESTAMPTZ,
    last_sync_status       TEXT,                    -- 'ok' | 'error' | NULL
    last_sync_error        TEXT,
    created_at             TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ  NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id)
);

-- One active config per tenant. Multiple inactive rows are allowed
-- (lets an admin draft a new config alongside the live one).
CREATE UNIQUE INDEX IF NOT EXISTS idx_ldap_configs_one_active
    ON ldap_configs (tenant_id) WHERE is_active;

CREATE TRIGGER update_ldap_configs_updated_at
    BEFORE UPDATE ON ldap_configs FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

ALTER TABLE ldap_configs ENABLE ROW LEVEL SECURITY;
ALTER TABLE ldap_configs FORCE  ROW LEVEL SECURITY;
DROP POLICY IF EXISTS ldap_configs_tenant_isolation        ON ldap_configs;
DROP POLICY IF EXISTS ldap_configs_tenant_isolation_insert ON ldap_configs;
CREATE POLICY ldap_configs_tenant_isolation ON ldap_configs
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY ldap_configs_tenant_isolation_insert ON ldap_configs
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);


-- ---- ldap_group_mappings -------------------------------------------------
-- Maps an AD/LDAP group DN to a DMS group. Unmapped LDAP groups are
-- silently ignored — no implicit pass-through ever creates a DMS
-- group.
CREATE TABLE IF NOT EXISTS ldap_group_mappings (
    tenant_id        UUID         NOT NULL REFERENCES organizations(id),
    ldap_config_id   UUID         NOT NULL,
    -- DN is case-insensitive in LDAP, but we store as-typed and
    -- compare lowered at runtime. Matches what AD admins see in
    -- "Active Directory Users and Computers".
    ldap_group_dn    TEXT         NOT NULL,
    dms_group_id     UUID         NOT NULL,
    created_at       TIMESTAMPTZ  NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, ldap_config_id, ldap_group_dn, dms_group_id),
    FOREIGN KEY (tenant_id, ldap_config_id) REFERENCES ldap_configs(tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, dms_group_id)   REFERENCES groups(tenant_id, id)       ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_ldap_group_mappings_lookup
    ON ldap_group_mappings (tenant_id, ldap_config_id);

ALTER TABLE ldap_group_mappings ENABLE ROW LEVEL SECURITY;
ALTER TABLE ldap_group_mappings FORCE  ROW LEVEL SECURITY;
DROP POLICY IF EXISTS ldap_group_mappings_tenant_isolation        ON ldap_group_mappings;
DROP POLICY IF EXISTS ldap_group_mappings_tenant_isolation_insert ON ldap_group_mappings;
CREATE POLICY ldap_group_mappings_tenant_isolation ON ldap_group_mappings
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY ldap_group_mappings_tenant_isolation_insert ON ldap_group_mappings
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);


-- ---- ldap_sync_history ---------------------------------------------------
-- Append-only audit of sync runs. The UI shows the last ~30 entries
-- so an admin can confirm "yes, sync ran 2 minutes ago and pulled
-- 142 users".
CREATE TABLE IF NOT EXISTS ldap_sync_history (
    tenant_id      UUID         NOT NULL REFERENCES organizations(id),
    id             UUID         NOT NULL DEFAULT gen_random_uuid(),
    ldap_config_id UUID         NOT NULL,
    -- 'scheduled' | 'manual' | 'login' (incremental on user login)
    trigger        TEXT         NOT NULL,
    started_at     TIMESTAMPTZ  NOT NULL DEFAULT now(),
    finished_at    TIMESTAMPTZ,
    -- 'ok' | 'partial' | 'error' | 'running'
    status         TEXT         NOT NULL DEFAULT 'running',
    users_synced   INTEGER      NOT NULL DEFAULT 0,
    groups_synced  INTEGER      NOT NULL DEFAULT 0,
    errors         INTEGER      NOT NULL DEFAULT 0,
    error_summary  TEXT,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, ldap_config_id) REFERENCES ldap_configs(tenant_id, id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_ldap_sync_history_recent
    ON ldap_sync_history (tenant_id, started_at DESC);

ALTER TABLE ldap_sync_history ENABLE ROW LEVEL SECURITY;
ALTER TABLE ldap_sync_history FORCE  ROW LEVEL SECURITY;
DROP POLICY IF EXISTS ldap_sync_history_tenant_isolation        ON ldap_sync_history;
DROP POLICY IF EXISTS ldap_sync_history_tenant_isolation_insert ON ldap_sync_history;
CREATE POLICY ldap_sync_history_tenant_isolation ON ldap_sync_history
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY ldap_sync_history_tenant_isolation_insert ON ldap_sync_history
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
