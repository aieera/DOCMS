//go:build integration

// Review M-8: POST /api/v1/auth/mfa/disable takes a TOTP (or recovery) code
// and a hit turns MFA off, yet it had no rate limiter in chi (nor in Kong:
// it falls under the plain auth-mfa-rest route). It now carries the same
// per-IP limiter class as the other TOTP-guessing routes (/mfa/verify,
// /mfa/recovery, /mfa/recovery-codes: burst 10, 5 per minute). Same check
// as TestAuthRouter_TrueClientIPIgnored: a burst from one client is
// limited within a dozen requests. The route needs a real session, hence
// the integration environment.
package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/aieera/sedoc/services/auth/internal/service"
	"github.com/aieera/sedoc/services/auth/internal/testenv"
)

func TestMFADisable_RateLimited(t *testing.T) {
	env := integrationEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	h, svc := newIntegrationHandler(t, env)
	router := h.Router(nil, nil, nil, nil, nil, nil, nil, nil, nil)

	slug := testenv.UniqueSlug("mfadisrl")
	tenant := testenv.SeedTenant(ctx, t, env.Pool, slug)
	hash, err := bcrypt.GenerateFromPassword([]byte(principalTestPassword), bcrypt.MinCost)
	require.NoError(t, err)
	email := "mfadis-" + uuid.NewString()[:8] + "@example.com"
	testenv.SeedUser(ctx, t, env.Pool, tenant, testenv.UserSpec{Email: email, PasswordHash: string(hash)})
	res, err := svc.Login(ctx, service.LoginInput{
		Email: email, Password: principalTestPassword, TenantSlug: slug,
		IPAddress: "203.0.113.9", UserAgent: "test",
	})
	require.NoError(t, err)
	session := res.Session.Token

	limitedWithin := func(n int) bool {
		for i := 0; i < n; i++ {
			req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/auth/mfa/disable",
				strings.NewReader(`{"totp_code":"000000"}`))
			req.RemoteAddr = "192.0.2.10:40000"
			req.Header.Set("Authorization", "Bearer "+session)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code == http.StatusTooManyRequests {
				return true
			}
			require.NotEqual(t, http.StatusUnauthorized, rec.Code, "the session must reach the route: %s", rec.Body.String())
		}
		return false
	}
	require.True(t, limitedWithin(12), "a burst of TOTP guesses on /mfa/disable must hit the per-IP limiter")
}
