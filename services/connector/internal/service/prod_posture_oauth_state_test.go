//go:build integration && prodposture
// +build integration,prodposture

// Connector OAuth state is single-use and PKCE-bound (connector migration
// 000002). Runs under the prod NOBYPASSRLS dms_app role against the
// connector's REAL migrations, with a local token endpoint standing in for
// Google.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/connector/internal/repository"
)

func newOAuthStateDB(ctx context.Context, t *testing.T) *testutil.ProdPostureDB {
	t.Helper()
	db := testutil.NewProdPostureDB(ctx, t, "")
	// connector_configs per document 000001 table 39 + 000045 uniqueness;
	// the connector's own migrations are then applied for real on top.
	_, err := db.Super.Exec(ctx, `
		CREATE TABLE organizations (id UUID PRIMARY KEY, deleted_at TIMESTAMPTZ);
		CREATE TABLE connector_configs (
			tenant_id              UUID NOT NULL REFERENCES organizations(id),
			id                     UUID NOT NULL DEFAULT gen_random_uuid(),
			connector_type         TEXT NOT NULL,
			display_name           TEXT NOT NULL,
			config_encrypted       JSONB NOT NULL DEFAULT '{}',
			oauth_tokens_encrypted JSONB,
			is_active              BOOLEAN NOT NULL DEFAULT true,
			last_sync_at           TIMESTAMPTZ,
			sync_status            TEXT,
			created_by             UUID,
			created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (tenant_id, id),
			UNIQUE (tenant_id, connector_type)
		);
		ALTER TABLE connector_configs ENABLE ROW LEVEL SECURITY;
		ALTER TABLE connector_configs FORCE  ROW LEVEL SECURITY;
		CREATE POLICY connector_configs_tenant_isolation ON connector_configs
			USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
			WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
		GRANT SELECT, INSERT, UPDATE, DELETE ON organizations, connector_configs TO dms_app;
	`)
	require.NoError(t, err)
	require.NoError(t, database.RunMigrations(db.SuperDSN, "../../migrations"),
		"connector migrations (incl. 000002) must apply cleanly")
	return db
}

// oauthHarness is a Service on the prod-posture DB with Google credentials
// saved and a local token endpoint that checks the PKCE verifier.
type oauthHarness struct {
	svc       *Service
	tenant    uuid.UUID
	exchanges *atomic.Int32
	challenge *atomic.Value
}

func newOAuthHarness(ctx context.Context, t *testing.T) *oauthHarness {
	t.Helper()
	db := newOAuthStateDB(ctx, t)
	tenant := uuid.New()
	_, err := db.Super.Exec(ctx, `INSERT INTO organizations (id) VALUES ($1)`, tenant)
	require.NoError(t, err)

	h := &oauthHarness{tenant: tenant, exchanges: &atomic.Int32{}, challenge: &atomic.Value{}}
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.exchanges.Add(1)
		require.NoError(t, r.ParseForm())
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != h.challenge.Load() {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at", "refresh_token": "rt", "expires_in": 3600, "scope": "drive",
		})
	}))
	t.Cleanup(tokenSrv.Close)

	h.svc = New(Config{Repo: repository.New(db.App), Logger: zerolog.Nop()})
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	h.svc.SetConnectorDeps(key, append([]byte(nil), key...), "https://app.example.test/api/v1/connectors/oauth/callback")
	h.svc.oauthTokenEndpoint = tokenSrv.URL
	require.NoError(t, h.svc.SaveGoogleConfig(ctx, tenant.String(), "", "client-id", "client-secret"))
	return h
}

// start runs StartGoogleOAuth as actor and returns the state.
func (h *oauthHarness) start(ctx context.Context, t *testing.T, actor string) string {
	t.Helper()
	authURL, err := h.svc.StartGoogleOAuth(ctx, h.tenant.String(), actor)
	require.NoError(t, err)
	u, err := url.Parse(authURL)
	require.NoError(t, err)
	require.Equal(t, "S256", u.Query().Get("code_challenge_method"))
	h.challenge.Store(u.Query().Get("code_challenge"))
	return u.Query().Get("state")
}

func TestGoogleCallback_ReplayedStateRejected(t *testing.T) {
	testutil.AssertProdPosture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)
	h := newOAuthHarness(ctx, t)
	admin := uuid.NewString()
	state := h.start(ctx, t, admin)

	target, err := h.svc.HandleGoogleOAuthCallback(ctx, "code-1", state, admin)
	require.NoError(t, err, "first callback completes")
	require.Equal(t, "/admin/integrations?connected=google", target)
	require.EqualValues(t, 1, h.exchanges.Load())

	_, err = h.svc.HandleGoogleOAuthCallback(ctx, "code-2", state, admin)
	require.True(t, errors.Is(err, ErrOAuthStateInvalid), "replayed state must be rejected, got %v", err)
	require.EqualValues(t, 1, h.exchanges.Load(), "a replayed state must never reach the token endpoint")

	// A correctly signed, unexpired state whose nonce was never issued
	// (e.g. minted with a leaked HMAC key) is rejected too.
	forged := h.svc.signConnectorStateWith(h.tenant.String(), "google", "00112233445566778899aabbccddeeff",
		strconv.FormatInt(time.Now().Add(5*time.Minute).Unix(), 10))
	_, err = h.svc.HandleGoogleOAuthCallback(ctx, "code-3", forged, admin)
	require.True(t, errors.Is(err, ErrOAuthStateInvalid), "unissued nonce must be rejected, got %v", err)

	// A state issued for Google cannot be consumed as M365.
	state = h.start(ctx, t, admin)
	_, err = h.svc.HandleM365OAuthCallback(ctx, "code-4", state, admin)
	require.True(t, errors.Is(err, ErrOAuthStateInvalid), "cross-connector state must be rejected, got %v", err)
}

// Connect-account CSRF: an admin's authorize URL completed by someone else
// (their own Google consent, their session or none) must not bind that
// account to the admin's tenant — and must not burn the admin's flow.
func TestGoogleCallback_DifferentUserRejected(t *testing.T) {
	testutil.AssertProdPosture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)
	h := newOAuthHarness(ctx, t)
	admin, other := uuid.NewString(), uuid.NewString()
	state := h.start(ctx, t, admin)

	_, err := h.svc.HandleGoogleOAuthCallback(ctx, "attacker-code", state, other)
	require.True(t, errors.Is(err, ErrOAuthStateNotYours), "another user's callback must be refused, got %v", err)
	_, err = h.svc.HandleGoogleOAuthCallback(ctx, "attacker-code", state, "")
	require.True(t, errors.Is(err, ErrOAuthStateNotYours), "a sessionless callback must be refused, got %v", err)
	require.EqualValues(t, 0, h.exchanges.Load(), "refused callbacks must never exchange a code")

	// The refused attempts did not consume the nonce: the admin can finish.
	_, err = h.svc.HandleGoogleOAuthCallback(ctx, "admin-code", state, admin)
	require.NoError(t, err)
	require.EqualValues(t, 1, h.exchanges.Load())

	// A flow cannot be started without an acting user.
	_, err = h.svc.StartGoogleOAuth(ctx, h.tenant.String(), "")
	require.Error(t, err)
}
