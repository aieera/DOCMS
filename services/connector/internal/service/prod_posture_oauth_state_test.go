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

func TestGoogleCallback_ReplayedStateRejected(t *testing.T) {
	testutil.AssertProdPosture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)
	db := newOAuthStateDB(ctx, t)

	tenant := uuid.New()
	_, err := db.Super.Exec(ctx, `INSERT INTO organizations (id) VALUES ($1)`, tenant)
	require.NoError(t, err)

	var exchanges atomic.Int32
	var wantChallenge atomic.Value
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exchanges.Add(1)
		require.NoError(t, r.ParseForm())
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != wantChallenge.Load() {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at", "refresh_token": "rt", "expires_in": 3600, "scope": "drive",
		})
	}))
	t.Cleanup(tokenSrv.Close)

	svc := New(Config{Repo: repository.New(db.App), Logger: zerolog.Nop()})
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	svc.SetConnectorDeps(key, append([]byte(nil), key...), "https://app.example.test/api/v1/connectors/oauth/callback")
	svc.oauthTokenEndpoint = tokenSrv.URL
	require.NoError(t, svc.SaveGoogleConfig(ctx, tenant.String(), "", "client-id", "client-secret"))

	authURL, err := svc.StartGoogleOAuth(ctx, tenant.String(), "")
	require.NoError(t, err)
	u, err := url.Parse(authURL)
	require.NoError(t, err)
	state := u.Query().Get("state")
	require.Equal(t, "S256", u.Query().Get("code_challenge_method"))
	wantChallenge.Store(u.Query().Get("code_challenge"))

	target, err := svc.HandleGoogleOAuthCallback(ctx, "code-1", state)
	require.NoError(t, err, "first callback completes")
	require.Equal(t, "/admin/integrations?connected=google", target)
	require.EqualValues(t, 1, exchanges.Load())

	_, err = svc.HandleGoogleOAuthCallback(ctx, "code-2", state)
	require.True(t, errors.Is(err, ErrOAuthStateInvalid), "replayed state must be rejected, got %v", err)
	require.EqualValues(t, 1, exchanges.Load(), "a replayed state must never reach the token endpoint")

	// A correctly signed, unexpired state whose nonce was never issued
	// (e.g. minted with a leaked HMAC key) is rejected too.
	forged := svc.signConnectorStateWith(tenant.String(), "google", "00112233445566778899aabbccddeeff",
		strconv.FormatInt(time.Now().Add(5*time.Minute).Unix(), 10))
	_, err = svc.HandleGoogleOAuthCallback(ctx, "code-3", forged)
	require.True(t, errors.Is(err, ErrOAuthStateInvalid), "unissued nonce must be rejected, got %v", err)

	// A state issued for Google cannot be consumed as M365.
	authURL, err = svc.StartGoogleOAuth(ctx, tenant.String(), "")
	require.NoError(t, err)
	u, _ = url.Parse(authURL)
	_, err = svc.HandleM365OAuthCallback(ctx, "code-4", u.Query().Get("state"))
	require.True(t, errors.Is(err, ErrOAuthStateInvalid), "cross-connector state must be rejected, got %v", err)
}
