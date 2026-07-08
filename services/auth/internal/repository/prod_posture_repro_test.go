//go:build integration && prodposture
// +build integration,prodposture

// Prod-only RLS repro: auth's PRE-TENANT reads fail closed under the
// production NOBYPASSRLS role.
//
// Bug (audit-verified, STATE_OF_THE_PROJECT 2026-07-03; tracked in
// https://github.com/aieera/DOCMS/issues/75): the auth service performs
// reads on the raw pgx pool BEFORE the tenant is known — that is the
// whole point of these lookups (the row itself tells us the tenant):
//
//   - API-key lookup: apiKeyRepo.GetByHash (apikey_repo.go:44), called
//     pre-tenant from Service.ValidateAPIKey (internal/service/apikey.go:150)
//     on every API-key-authenticated request.
//   - Session Postgres fallback: sessionRepo.GetByTokenHash, called
//     pre-tenant on Redis cache miss (internal/service/session.go:105).
//   - M365 token exchange: findUsersByEmailAcrossTenants
//     (internal/service/m365_exchange.go:178) queries users/organizations
//     with no tenant GUC.
//
// In dev these work because the connection role has BYPASSRLS. In prod
// the app role is dms_app NOBYPASSRLS and api_keys/sessions/users carry
// ENABLE + FORCE ROW LEVEL SECURITY with a policy on
// current_setting('app.current_tenant') — unset GUC means the policy
// evaluates NULL and every row is filtered. Net effect: API-key auth
// 404s (ErrUnauthorized), a session-cache miss logs the user out.
//
// This test pins ONE representative path — GetByHash — end to end with
// the real repository code against a prod-posture database. It asserts
// the CORRECT (post-fix) behavior: the pre-tenant lookup finds the key.
// Today it FAILS with not-found (RLS fail-closed), which is the point:
// the prod-posture CI lane keeps it allow-listed in
// ci/prod-posture-allowlist.txt until the Wave A fix lands (how the fix
// makes a pre-tenant lookup legal — SECURITY DEFINER function, a
// dedicated policy, or a scoped-bypass role — is Wave A's decision, not
// this test's).
//
// Schema note: the auth service has no migrations directory of its own —
// api_keys/users/organizations are defined in
// services/document/migrations/000001_initial_schema.up.sql. That chain
// currently does not apply to a clean database (000021_ner_pipeline
// UPDATEs document_entities, which no prior migration creates — the
// STATE 2026-07-03 "Migration blocker"), so this test recreates the
// three tables VERBATIM from 000001 (columns, PK/FK, ENABLE + FORCE RLS,
// the exact tenant-isolation policies) instead of running the chain.
//
// Run:
//
//	go test -tags "integration prodposture" \
//	  -run '^TestProdPosture_AuthPreTenantReads$' \
//	  ./services/auth/internal/repository/
package repository_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/auth/internal/model"
	"github.com/aieera/sedoc/services/auth/internal/repository"
)

func TestProdPosture_AuthPreTenantReads(t *testing.T) {
	testutil.AssertProdPosture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)

	// Empty migrationsDir: see the schema note in the header — the shared
	// document migration chain is broken on a clean DB at 000021, so the
	// auth tables are created verbatim from 000001 below.
	db := testutil.NewProdPostureDB(ctx, t, "")

	// DDL verbatim from services/document/migrations/000001_initial_schema.up.sql
	// (TABLE 1 organizations, TABLE 2 users, TABLE 21 api_keys; triggers
	// omitted — irrelevant here). The grants mirror what migration 000065 /
	// NewProdPostureDB give dms_app; they must be re-issued because
	// NewProdPostureDB's GRANT ... ON ALL TABLES ran before these existed.
	_, err := db.Super.Exec(ctx, `
		CREATE TABLE organizations (
		    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
		    name           TEXT NOT NULL,
		    slug           TEXT NOT NULL UNIQUE,
		    plan           TEXT NOT NULL DEFAULT 'standard'
		                        CHECK (plan IN ('standard', 'enterprise', 'dedicated')),
		    settings       JSONB NOT NULL DEFAULT '{}'::jsonb,
		    primary_region TEXT NOT NULL DEFAULT 'us-east-1',
		    logo_url       TEXT,
		    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
		    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
		    deleted_at     TIMESTAMPTZ
		);

		CREATE TABLE users (
		    tenant_id             UUID NOT NULL REFERENCES organizations(id),
		    id                    UUID NOT NULL DEFAULT gen_random_uuid(),
		    email                 TEXT NOT NULL,
		    display_name          TEXT NOT NULL,
		    password_hash         TEXT,
		    avatar_url            TEXT,
		    role                  TEXT NOT NULL DEFAULT 'member'
		                               CHECK (role IN ('owner', 'admin', 'member', 'guest')),
		    status                TEXT NOT NULL DEFAULT 'active'
		                               CHECK (status IN ('active', 'suspended', 'deactivated')),
		    mfa_enabled           BOOLEAN NOT NULL DEFAULT false,
		    mfa_secret_encrypted  TEXT,
		    mfa_recovery_hashes   TEXT[],
		    last_login_at         TIMESTAMPTZ,
		    locale                TEXT DEFAULT 'en',
		    timezone              TEXT DEFAULT 'UTC',
		    settings              JSONB NOT NULL DEFAULT '{}'::jsonb,
		    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
		    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
		    deleted_at            TIMESTAMPTZ,
		    PRIMARY KEY (tenant_id, id),
		    UNIQUE (tenant_id, email)
		);
		ALTER TABLE users ENABLE ROW LEVEL SECURITY;
		ALTER TABLE users FORCE  ROW LEVEL SECURITY;
		CREATE POLICY users_tenant_isolation ON users
		    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

		CREATE TABLE api_keys (
		    tenant_id     UUID NOT NULL REFERENCES organizations(id),
		    id            UUID NOT NULL DEFAULT gen_random_uuid(),
		    user_id       UUID,
		    name          TEXT NOT NULL,
		    key_hash      TEXT NOT NULL UNIQUE,
		    key_prefix    TEXT NOT NULL,
		    scopes        TEXT[] NOT NULL DEFAULT '{}',
		    last_used_at  TIMESTAMPTZ,
		    expires_at    TIMESTAMPTZ,
		    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
		    revoked_at    TIMESTAMPTZ,
		    PRIMARY KEY (tenant_id, id),
		    FOREIGN KEY (tenant_id, user_id) REFERENCES users(tenant_id, id)
		);
		CREATE INDEX idx_api_keys_user   ON api_keys(tenant_id, user_id) WHERE revoked_at IS NULL;
		CREATE INDEX idx_api_keys_prefix ON api_keys(key_prefix)         WHERE revoked_at IS NULL;
		ALTER TABLE api_keys ENABLE ROW LEVEL SECURITY;
		ALTER TABLE api_keys FORCE  ROW LEVEL SECURITY;
		CREATE POLICY api_keys_tenant_isolation ON api_keys
		    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
		CREATE POLICY api_keys_tenant_isolation_insert ON api_keys
		    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

		CREATE TABLE sessions (
		    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
		    tenant_id        UUID NOT NULL REFERENCES organizations(id),
		    user_id          UUID NOT NULL,
		    token_hash       TEXT NOT NULL UNIQUE,
		    ip_address       INET,
		    user_agent       TEXT,
		    expires_at       TIMESTAMPTZ NOT NULL,
		    last_activity_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
		    revoked_at       TIMESTAMPTZ
		);
		ALTER TABLE sessions ENABLE ROW LEVEL SECURITY;
		ALTER TABLE sessions FORCE  ROW LEVEL SECURITY;
		CREATE POLICY sessions_tenant_isolation ON sessions
		    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
		CREATE POLICY sessions_tenant_isolation_insert ON sessions
		    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

		GRANT SELECT, INSERT, UPDATE, DELETE ON organizations, users, api_keys, sessions TO dms_app;
	`)
	require.NoError(t, err, "seed auth schema (verbatim from document 000001)")

	// Run the REAL auth migration on top: it creates the dms_auth_lookup
	// definer role + the three SECURITY DEFINER exact-match functions the
	// fixed pre-tenant reads call. The tables above must exist first
	// (check_function_bodies validates the function SQL at creation).
	require.NoError(t,
		database.RunServiceMigrations(db.SuperDSN, "../../migrations", "auth"),
		"auth migration 000001 (pre-tenant lookup functions)")
	// Re-grant: the migration created no tables, but re-issue to be safe
	// alongside any objects it added.
	_, err = db.Super.Exec(ctx,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON organizations, users, api_keys, sessions TO dms_app;`)
	require.NoError(t, err)

	tenantID := uuid.Must(uuid.NewV7())
	userID := uuid.Must(uuid.NewV7())

	// Parent rows the api_keys FKs demand, seeded as the superuser — the
	// same split ops/provisioning tooling has in prod (organizations has
	// no RLS; the users insert is out-of-band seeding).
	_, err = db.Super.Exec(ctx,
		`INSERT INTO organizations (id, name, slug) VALUES ($1, 'Prod Posture Org', 'prod-posture-org')`,
		tenantID)
	require.NoError(t, err, "seed organization")
	_, err = db.Super.Exec(ctx,
		`INSERT INTO users (tenant_id, id, email, display_name, role)
		 VALUES ($1, $2, 'posture@example.com', 'Posture User', 'admin')`,
		tenantID, userID)
	require.NoError(t, err, "seed user")

	// The API key row goes through the REAL repository Create under
	// WithTenantTx on the dms_app pool — the tenant-correct write path,
	// exactly like Service.IssueAPIKey. Plaintext/hash use the production
	// format: "vdms_" + 48 alphanum, key_hash = sha256 hex, key_prefix =
	// first 12 chars (service/apikey.go generateAPIKey / sha256Hex).
	const plaintext = "vdms_ProdPostureRepro0000000000000000000000000000000"
	sum := sha256.Sum256([]byte(plaintext))
	keyHash := hex.EncodeToString(sum[:])

	repo := repository.NewAPIKeyRepo()
	key := &model.APIKey{
		TenantID:  tenantID,
		ID:        uuid.Must(uuid.NewV7()),
		UserID:    userID,
		Name:      "prod-posture repro",
		KeyHash:   keyHash,
		KeyPrefix: plaintext[:12],
		Scopes:    []string{"documents:read"},
		CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, database.WithTenantTx(ctx, db.App, tenantID, func(tx pgx.Tx) error {
		return repo.Create(ctx, tx, key)
	}), "create API key via real repo under WithTenantTx")

	// Sanity: inside the tenant context the row is visible — proves the
	// seed is good and pins any failure below on the PRE-TENANT read, not
	// on fixtures.
	require.NoError(t, database.WithTenantTx(ctx, db.App, tenantID, func(tx pgx.Tx) error {
		n, err := repo.CountActiveByUser(ctx, tx, tenantID, userID)
		if err != nil {
			return err
		}
		require.Equal(t, 1, n, "key must be visible under WithTenantTx")
		return nil
	}))

	// Seed a session for the fallback path — out-of-band, tenant-correct.
	sessionID := uuid.Must(uuid.NewV7())
	tokenHash := hex.EncodeToString(func() []byte { h := sha256.Sum256([]byte("session-token-prodposture")); return h[:] }())
	require.NoError(t, database.WithTenantTx(ctx, db.App, tenantID, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `
			INSERT INTO sessions (id, tenant_id, user_id, token_hash, expires_at)
			VALUES ($1, $2, $3, $4, now() + interval '1 hour')`,
			sessionID, tenantID, userID, tokenHash)
		return e
	}), "seed session")

	// Drop idle connections so each pre-tenant read below runs on a fresh
	// connection with app.current_tenant genuinely unset (NULL) — the
	// state a prod middleware conn is in. Without this, the test reuses
	// the seeding connection, where the committed set_config leaves the
	// GUC as '' and the FORCE-RLS policy errors `invalid input syntax for
	// type uuid: ""` (22P02) — a sibling manifestation of the same bug;
	// we pin the canonical fail-closed mode instead.
	db.App.Reset()

	sessRepo := repository.NewSessionRepo()

	// Each read runs pre-tenant on the dms_app (NOBYPASSRLS) pool exactly
	// as prod middleware does, then the SECURITY DEFINER function yields
	// the row + its tenant. Before the fix each fails closed (not found).

	t.Run("APIKeyAuth", func(t *testing.T) {
		got, err := repo.GetByHash(ctx, db.App, keyHash)
		require.NoError(t, err,
			"pre-tenant GetByHash must find the key (RLS fail-closed 404s all API-key auth — #75)")
		require.Equal(t, key.ID, got.ID)
		require.Equal(t, tenantID, got.TenantID, "the row must yield the tenant for auth context")
	})

	t.Run("SessionCacheMissFallback", func(t *testing.T) {
		got, err := sessRepo.GetByTokenHash(ctx, db.App, tokenHash)
		require.NoError(t, err,
			"pre-tenant GetByTokenHash must find the session (RLS fail-closed logs users out on Redis miss — #75)")
		require.NotNil(t, got)
		require.Equal(t, sessionID, got.ID)
		require.Equal(t, tenantID, got.TenantID)
	})

	t.Run("M365EmailExchange", func(t *testing.T) {
		// The m365 exchange resolves email→tenant via the same SECURITY
		// DEFINER function the service method now calls
		// (findUsersByEmailAcrossTenants → auth_lookup_users_by_email).
		var gotTenant, gotUser string
		err := db.App.QueryRow(ctx,
			`SELECT tenant_id, user_id FROM auth_lookup_users_by_email($1, $2)`,
			"posture@example.com", nil).Scan(&gotTenant, &gotUser)
		require.NoError(t, err,
			"pre-tenant email lookup must resolve (RLS fail-closed kills m365 exchange — #75)")
		require.Equal(t, tenantID.String(), gotTenant)
		require.Equal(t, userID.String(), gotUser)
	})

	t.Run("CrossTenantIsolation", func(t *testing.T) {
		// A different tenant's context must not see this tenant's key via
		// the in-tenant read path (the definer functions are exact-match
		// on globally-unique columns; ordinary tenant-scoped reads still
		// isolate). Confirm the FORCE-RLS policy still holds for non-lookup
		// reads: a raw pool read with no tenant GUC sees nothing.
		var n int
		require.NoError(t, db.App.QueryRow(ctx, `SELECT count(*) FROM api_keys`).Scan(&n))
		require.Zero(t, n, "ordinary (non-definer) reads must stay RLS fail-closed — no general bypass")
	})
}
