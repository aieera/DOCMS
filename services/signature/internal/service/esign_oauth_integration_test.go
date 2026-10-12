//go:build integration

// S-2: e-sign OAuth connect is an admin action, and its state is a random,
// single-use, 10-minute nonce bound to the tenant and the admin who started
// the flow — a replayed, foreign or expired state completes nothing.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/esign"
)

// fakeDocuSign serves the token exchange and userinfo endpoints and counts
// token exchanges.
func fakeDocuSign(t *testing.T) (*httptest.Server, *int32) {
	t.Helper()
	var exchanges int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			atomic.AddInt32(&exchanges, 1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "at", "refresh_token": "rt", "expires_in": 3600, "scope": "signature",
			})
		case "/oauth/userinfo":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"accounts": []map[string]any{{"account_id": "acc-1", "is_default": true, "base_uri": "https://demo.docusign.net"}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &exchanges
}

func (f *svcFixture) wireOAuth(t *testing.T, srv *httptest.Server) {
	t.Helper()
	secret := []byte("0123456789abcdef0123456789abcdef")
	f.svc.AddESign(ESignConfig{
		SealingKey: secret,
		OAuthByProvider: map[esign.Provider]esign.OAuthConfig{
			esign.ProviderDocuSign: {
				Provider: esign.ProviderDocuSign, AuthorizeURL: srv.URL + "/oauth/auth",
				TokenURL: srv.URL + "/oauth/token", ClientID: "cid", ClientSecret: "csecret",
				RedirectURI: "https://app.test/api/v1/signatures/esign/oauth/callback",
				Scope:       "signature", HMACSecret: secret,
			},
		},
		DefaultHMACSecret: secret,
		HTTPClient:        srv.Client(),
	})
}

func stateFrom(t *testing.T, redirect string) string {
	t.Helper()
	u, err := url.Parse(redirect)
	require.NoError(t, err)
	st := u.Query().Get("state")
	require.NotEmpty(t, st)
	return st
}

func TestEsignOAuthStart_MemberForbidden(t *testing.T) {
	f := newSvcFixture(t)
	srv, _ := fakeDocuSign(t)
	f.wireOAuth(t, srv)

	member := Actor{TenantID: f.tenant.ID.String(), UserID: f.tenant.Member.String(), Role: "member"}
	_, err := f.svc.StartOAuth(context.Background(), member, esign.ProviderDocuSign)
	require.True(t, errors.Is(err, ErrForbidden), "a member must not start an e-sign connect, got %v", err)

	_, err = f.svc.HandleOAuthCallback(context.Background(), member, "code", "anything")
	require.True(t, errors.Is(err, ErrForbidden), "a member must not complete an e-sign connect, got %v", err)

	err = f.svc.Disconnect(context.Background(), member, esign.ProviderDocuSign)
	require.True(t, errors.Is(err, ErrForbidden), "a member must not disconnect the tenant's e-sign account, got %v", err)
}

func TestEsignOAuthCallback_ReplayedStateRejected(t *testing.T) {
	f := newSvcFixture(t)
	srv, exchanges := fakeDocuSign(t)
	f.wireOAuth(t, srv)
	ctx := context.Background()
	owner := f.ownerActor()

	redirect, err := f.svc.StartOAuth(ctx, owner, esign.ProviderDocuSign)
	require.NoError(t, err)
	state := stateFrom(t, redirect)

	otherID, _ := seedUserRole(t, f, "admin")
	other := Actor{TenantID: f.tenant.ID.String(), UserID: otherID, Role: "admin"}
	_, err = f.svc.HandleOAuthCallback(ctx, other, "code", state)
	require.Error(t, err, "a state started by another admin must not be accepted")

	_, err = f.svc.HandleOAuthCallback(ctx, owner, "code", state)
	require.NoError(t, err, "the starting admin's first callback succeeds")
	require.EqualValues(t, 1, atomic.LoadInt32(exchanges))

	_, err = f.svc.HandleOAuthCallback(ctx, owner, "code", state)
	require.Error(t, err, "a replayed state must be rejected")
	require.EqualValues(t, 1, atomic.LoadInt32(exchanges), "a replay must not reach the vendor token endpoint")

	// The legacy deterministic HMAC state is not accepted either.
	legacy := esign.OAuthConfig{Provider: esign.ProviderDocuSign, HMACSecret: []byte("0123456789abcdef0123456789abcdef")}
	_, legacyState, err := legacy.AuthorizeURLBuilder(f.tenant.ID.String())
	require.NoError(t, err)
	_, err = f.svc.HandleOAuthCallback(ctx, owner, "code", legacyState)
	require.Error(t, err, "a deterministic HMAC state must not complete a connect")

	// An expired nonce is rejected.
	redirect, err = f.svc.StartOAuth(ctx, owner, esign.ProviderDocuSign)
	require.NoError(t, err)
	expired := stateFrom(t, redirect)
	_, err = f.db.Super.Exec(ctx, `UPDATE esign_oauth_states SET expires_at = now() - interval '1 second' WHERE tenant_id = $1`, f.tenant.ID)
	require.NoError(t, err)
	_, err = f.svc.HandleOAuthCallback(ctx, owner, "code", expired)
	require.Error(t, err, "an expired state must be rejected")
	require.EqualValues(t, 1, atomic.LoadInt32(exchanges))
}
