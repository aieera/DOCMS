//go:build integration
// +build integration

// Session revocation vs the Redis fast path (ADR 0123).
//
// Regression: ValidateSession's Redis fast path returned the cached
// identity without re-checking revoked_at / user status, and revoke /
// suspend never cleared the cache — so a revoked session or suspended
// user kept authorizing for up to the 24h TTL. These tests drive the
// real Service against real Postgres + Redis and pin:
//   - RevokeSession → the next fast-path read is 401 (active invalidation);
//   - SuspendUser → every session dead AND every API key revoked;
//   - the self-heal window forces revalidation of a still-cached but
//     DB-revoked session even when active invalidation is bypassed;
//   - the cache-miss fallback still authorizes a genuinely valid session.
package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/auth/internal/model"
	"github.com/aieera/sedoc/services/auth/internal/repository"
)

// revokeFixtureDDL — organizations + users + sessions + api_keys
// (verbatim from document 000001) plus the SECURITY DEFINER
// auth_lookup_session_by_token the Postgres fallback calls (auth
// 000001, which filters revoked_at IS NULL). No RLS: the container
// superuser drives the flow.
const revokeFixtureDDL = `
CREATE TABLE organizations (id UUID PRIMARY KEY, deleted_at TIMESTAMPTZ);
CREATE TABLE users (
	tenant_id           UUID NOT NULL REFERENCES organizations(id),
	id                  UUID NOT NULL,
	email               TEXT NOT NULL,
	display_name        TEXT NOT NULL DEFAULT '',
	password_hash       TEXT,
	avatar_url          TEXT,
	role                TEXT NOT NULL DEFAULT 'member',
	status              TEXT NOT NULL DEFAULT 'active',
	mfa_enabled         BOOLEAN NOT NULL DEFAULT false,
	mfa_secret_encrypted TEXT,
	mfa_recovery_hashes  TEXT[] NOT NULL DEFAULT '{}',
	last_login_at       TIMESTAMPTZ,
	locale              TEXT NOT NULL DEFAULT 'en',
	timezone            TEXT NOT NULL DEFAULT 'UTC',
	settings            JSONB NOT NULL DEFAULT '{}'::jsonb,
	created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
	deleted_at          TIMESTAMPTZ,
	PRIMARY KEY (tenant_id, id)
);
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
	revoked_at       TIMESTAMPTZ,
	FOREIGN KEY (tenant_id, user_id) REFERENCES users(tenant_id, id)
);
CREATE TABLE api_keys (
	tenant_id    UUID NOT NULL REFERENCES organizations(id),
	id           UUID NOT NULL DEFAULT gen_random_uuid(),
	user_id      UUID,
	name         TEXT NOT NULL,
	key_hash     TEXT NOT NULL UNIQUE,
	key_prefix   TEXT NOT NULL,
	scopes       TEXT[] NOT NULL DEFAULT '{}',
	last_used_at TIMESTAMPTZ,
	expires_at   TIMESTAMPTZ,
	created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
	revoked_at   TIMESTAMPTZ,
	PRIMARY KEY (tenant_id, id),
	FOREIGN KEY (tenant_id, user_id) REFERENCES users(tenant_id, id)
);
CREATE TABLE outbox (
	id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	tenant_id      UUID NOT NULL,
	event_type     TEXT NOT NULL,
	aggregate_type TEXT NOT NULL,
	aggregate_id   UUID NOT NULL,
	payload        JSONB NOT NULL,
	published      BOOLEAN NOT NULL DEFAULT false,
	created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
	published_at   TIMESTAMPTZ,
	actor_id       UUID,
	actor_name     TEXT,
	ip_address     INET,
	user_agent     TEXT
);
CREATE FUNCTION auth_lookup_session_by_token(p_hash text)
RETURNS TABLE (
	id uuid, tenant_id uuid, user_id uuid, token_hash text,
	ip_address text, user_agent text, expires_at timestamptz,
	last_activity_at timestamptz, created_at timestamptz, revoked_at timestamptz
) LANGUAGE sql STABLE AS $$
	SELECT s.id, s.tenant_id, s.user_id, s.token_hash,
	       COALESCE(host(s.ip_address), ''), COALESCE(s.user_agent, ''),
	       s.expires_at, s.last_activity_at, s.created_at, s.revoked_at
	FROM sessions s WHERE s.token_hash = p_hash AND s.revoked_at IS NULL
$$;
`

type revokeHarness struct {
	ctx    context.Context
	svc    *Service
	rdb    *redis.Client
	tenant uuid.UUID
	user   uuid.UUID
}

func newRevokeHarness(t *testing.T) *revokeHarness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	t.Cleanup(cancel)

	dsn, pgClean, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(pgClean)
	cfg := database.DefaultPoolConfig()
	cfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, revokeFixtureDDL)
	require.NoError(t, err)

	redisURL, redisClean, err := testutil.NewRedisContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(redisClean)
	ropts, err := redis.ParseURL(redisURL)
	require.NoError(t, err)
	rdb := redis.NewClient(ropts)
	t.Cleanup(func() { _ = rdb.Close() })

	tenant := uuid.Must(uuid.NewV7())
	user := uuid.Must(uuid.NewV7())
	_, err = pool.Exec(ctx, `INSERT INTO organizations (id) VALUES ($1)`, tenant)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO users (tenant_id, id, email, role) VALUES ($1,$2,$3,'member')`,
		tenant, user, "u@test.local")
	require.NoError(t, err)

	svc := &Service{
		pool:     pool,
		rdb:      rdb,
		users:    repository.NewUserRepo(),
		sessions: repository.NewSessionRepo(),
		apiKeys:  repository.NewAPIKeyRepo(),
		outbox:   database.NewOutboxRepository(),
		now:      time.Now,
	}
	return &revokeHarness{ctx: ctx, svc: svc, rdb: rdb, tenant: tenant, user: user}
}

// login creates a real session row + primes the cache the way the login
// flow does, returning the plaintext token.
func (h *revokeHarness) login(t *testing.T) (token string, sessionID uuid.UUID) {
	t.Helper()
	u := &model.User{ID: h.user, TenantID: h.tenant, Email: "u@test.local", Role: "member", Status: model.StatusActive}
	var created *CreatedSession
	require.NoError(t, database.WithTenantTx(h.ctx, h.svc.pool, h.tenant, func(tx pgx.Tx) error {
		c, err := h.svc.createSessionInTx(h.ctx, tx, u, "127.0.0.1", "test")
		created = c
		return err
	}))
	return created.Token, created.Session.ID
}

func (h *revokeHarness) makeAPIKey(t *testing.T) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := h.svc.pool.Exec(h.ctx, `
		INSERT INTO api_keys (tenant_id, id, user_id, name, key_hash, key_prefix)
		VALUES ($1,$2,$3,'test','hash-`+id.String()+`','vdms_')`,
		h.tenant, id, h.user)
	require.NoError(t, err)
	return id
}

func (h *revokeHarness) apiKeyRevoked(t *testing.T, id uuid.UUID) bool {
	t.Helper()
	var revoked *time.Time
	require.NoError(t, h.svc.pool.QueryRow(h.ctx,
		`SELECT revoked_at FROM api_keys WHERE tenant_id=$1 AND id=$2`, h.tenant, id).Scan(&revoked))
	return revoked != nil
}

func TestRevokeSession_InvalidatesFastPathImmediately(t *testing.T) {
	h := newRevokeHarness(t)
	token, sessionID := h.login(t)

	// Warm the fast path.
	c, err := h.svc.ValidateSession(h.ctx, token)
	require.NoError(t, err)
	require.Equal(t, h.user, c.UserID)

	// Admin revokes the session.
	require.NoError(t, h.svc.RevokeSession(h.ctx, h.tenant, h.user, sessionID))

	// The very next validation must fail — the cache was actively
	// cleared and the DB row is revoked (the DEFINER fn filters it).
	_, err = h.svc.ValidateSession(h.ctx, token)
	require.ErrorIs(t, err, vdmserr.ErrUnauthorized, "revoked session must not authorize on the next request")
}

func TestSuspendUser_KillsAllSessionsAndAPIKeys(t *testing.T) {
	h := newRevokeHarness(t)
	tokenA, _ := h.login(t)
	tokenB, _ := h.login(t)
	keyID := h.makeAPIKey(t)

	// Warm both sessions.
	_, err := h.svc.ValidateSession(h.ctx, tokenA)
	require.NoError(t, err)
	_, err = h.svc.ValidateSession(h.ctx, tokenB)
	require.NoError(t, err)

	admin := uuid.Must(uuid.NewV7())
	_, err = h.svc.pool.Exec(h.ctx, `INSERT INTO users (tenant_id, id, email, role) VALUES ($1,$2,$3,'admin')`,
		h.tenant, admin, "admin@test.local")
	require.NoError(t, err)
	require.NoError(t, h.svc.SuspendUser(h.ctx, h.tenant, admin, h.user))

	// Both sessions dead immediately.
	_, err = h.svc.ValidateSession(h.ctx, tokenA)
	require.ErrorIs(t, err, vdmserr.ErrUnauthorized)
	_, err = h.svc.ValidateSession(h.ctx, tokenB)
	require.ErrorIs(t, err, vdmserr.ErrUnauthorized)

	// And the API key is revoked (part 3 — programmatic access dies too).
	require.True(t, h.apiKeyRevoked(t, keyID), "suspended user's API key must be revoked")
}

func TestSelfHealWindow_RevalidatesStaleCachedSession(t *testing.T) {
	h := newRevokeHarness(t)
	token, _ := h.login(t)
	hash := sha256Hex(token)

	// Warm the cache, then simulate a MISSED active invalidation: the DB
	// row is revoked directly but the cache entry is left in place.
	_, err := h.svc.ValidateSession(h.ctx, token)
	require.NoError(t, err)
	_, err = h.svc.pool.Exec(h.ctx, `UPDATE sessions SET revoked_at = now() WHERE token_hash = $1`, hash)
	require.NoError(t, err)

	// Within the trust window the (stale) cache still serves — this is
	// the accepted bounded exposure.
	_, err = h.svc.ValidateSession(h.ctx, token)
	require.NoError(t, err, "inside the window the cache is trusted")

	// Age the cache entry past FastPathRevalidateInterval by rewriting
	// LastChecked into the past, then the fast path MUST fall through to
	// Postgres (which filters the revoked row) → 401. This is the
	// self-heal that bounds a missed invalidation.
	c, err := h.svc.readCachedSession(h.ctx, hash)
	require.NoError(t, err)
	require.NotNil(t, c)
	c.LastChecked = time.Now().Add(-2 * FastPathRevalidateInterval)
	h.svc.cacheSession(h.ctx, hash, c)

	_, err = h.svc.ValidateSession(h.ctx, token)
	require.ErrorIs(t, err, vdmserr.ErrUnauthorized,
		"past the trust window a revoked session must be caught by revalidation")
}

func TestCacheMissFallback_ValidSessionStillAuthorizes(t *testing.T) {
	h := newRevokeHarness(t)
	token, _ := h.login(t)
	hash := sha256Hex(token)

	// Blow away the cache entirely (Redis eviction / cold instance).
	h.svc.deleteCachedSession(h.ctx, hash)
	require.NoError(t, h.rdb.Del(h.ctx, userSessionsKey(h.tenant, h.user)).Err())

	// The Postgres fallback must still authorize a genuinely valid,
	// non-revoked session (A.1.f) — and re-prime the cache.
	c, err := h.svc.ValidateSession(h.ctx, token)
	require.NoError(t, err, "cache-miss fallback must authorize a valid session")
	require.Equal(t, h.user, c.UserID)

	cached, err := h.svc.readCachedSession(h.ctx, hash)
	require.NoError(t, err)
	require.NotNil(t, cached, "fallback must re-prime the cache")
}
