//go:build integration

// DEF-01 — the recovery-codes page re-ran POST /mfa/setup, which replaced
// the TOTP secret of an ENABLED factor and locked the user out of their
// authenticator. The page now calls POST /mfa/recovery-codes (current TOTP
// code required; only the code hashes change), and /mfa/setup answers 409
// while MFA is on.
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/aieera/sedoc/services/auth/internal/service"
	"github.com/aieera/sedoc/services/auth/internal/testenv"
)

func TestMFARecoveryCodes_HTTP(t *testing.T) {
	env := integrationEnv(t)
	// Generous: setup and regeneration each bcrypt eight codes at cost 12,
	// which takes tens of seconds under -race.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	h, svc := newIntegrationHandler(t, env)
	router := h.Router(nil, nil, nil, nil, nil, nil, nil, nil, nil)

	slug := testenv.UniqueSlug("regenhttp")
	tenant := testenv.SeedTenant(ctx, t, env.Pool, slug)
	hash, err := bcrypt.GenerateFromPassword([]byte(principalTestPassword), bcrypt.MinCost)
	require.NoError(t, err)
	email := "regen-" + uuid.NewString()[:8] + "@example.com"
	user := testenv.SeedUser(ctx, t, env.Pool, tenant, testenv.UserSpec{Email: email, PasswordHash: string(hash)})

	res, err := svc.Login(ctx, service.LoginInput{
		Email: email, Password: principalTestPassword, TenantSlug: slug,
		IPAddress: "203.0.113.9", UserAgent: "test",
	})
	require.NoError(t, err)
	require.NotNil(t, res.Session)
	session := res.Session.Token

	post := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+session)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	// Enroll TOTP the normal way: setup → confirm.
	rec := post("/api/v1/auth/mfa/setup", ``)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var setup struct {
		Secret string `json:"secret"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &setup))
	now := time.Now()
	code, err := totp.GenerateCode(setup.Secret, now)
	require.NoError(t, err)
	rec = post("/api/v1/auth/mfa/confirm", `{"totp_code":"`+code+`"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var secretBefore string
	require.NoError(t, env.Pool.QueryRow(ctx,
		`SELECT mfa_secret_encrypted FROM users WHERE tenant_id = $1 AND id = $2`, tenant, user).Scan(&secretBefore))

	// Setup again while enabled is refused — no client can hit the lockout.
	rec = post("/api/v1/auth/mfa/setup", ``)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())

	// Wrong code → 400 on totp_code.
	rec = post("/api/v1/auth/mfa/recovery-codes", `{"totp_code":"12345x"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	var verr map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &verr))
	require.Equal(t, "totp_code", verr["field"])

	// Right code (next time-step: the confirm burned the current one) → 200.
	next, err := totp.GenerateCode(setup.Secret, now.Add(30*time.Second))
	require.NoError(t, err)
	rec = post("/api/v1/auth/mfa/recovery-codes", `{"totp_code":"`+next+`"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out.RecoveryCodes, 8)

	var (
		secretAfter string
		enabled     bool
	)
	require.NoError(t, env.Pool.QueryRow(ctx,
		`SELECT mfa_secret_encrypted, mfa_enabled FROM users WHERE tenant_id = $1 AND id = $2`, tenant, user).
		Scan(&secretAfter, &enabled))
	require.Equal(t, secretBefore, secretAfter, "regeneration must not touch the TOTP secret")
	require.True(t, enabled)
}

// An API key cannot regenerate its owner's recovery codes (DEC-14: account
// credential routes need a session principal).
func TestMFARecoveryCodes_APIKeyForbidden(t *testing.T) {
	f := newPrincipalFixture(t)
	rec := f.do(t, http.MethodPost, "/api/v1/auth/mfa/recovery-codes", f.apiKey, `{"totp_code":"000000"}`)
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
}
