//go:build integration

// L-2 — a TOTP code is single-use. Before the fix any code observed by an
// attacker (shoulder-surfed, phished in real time, logged) stayed valid
// for the whole ±1-step skew window (~90 s) and could complete a second
// login. The accepted time-step is now remembered per user.
package service

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/auth/internal/testenv"
)

func TestTOTP_ReplayRejected(t *testing.T) {
	env := integrationEnv(t)
	ctx := testCtx(t)
	svc := newIntegrationService(t, env)

	key, err := totp.Generate(totp.GenerateOpts{Issuer: "SeDoc", AccountName: "totp@example.com"})
	require.NoError(t, err)
	sealed, err := svc.encryptMFASecret(key.Secret())
	require.NoError(t, err)
	hash, err := bcrypt.GenerateFromPassword([]byte("Pa55word!long"), bcrypt.MinCost)
	require.NoError(t, err)

	slug := testenv.UniqueSlug("totp")
	tenant := testenv.SeedTenant(ctx, t, env.Pool, slug)
	email := "totp-" + uuid.NewString()[:8] + "@example.com"
	userID := testenv.SeedUser(ctx, t, env.Pool, tenant, testenv.UserSpec{
		Email: email, PasswordHash: string(hash), MFAEnabled: true, MFASecretEnc: sealed,
	})

	login := func() string {
		res, err := svc.Login(ctx, LoginInput{Email: email, Password: "Pa55word!long", TenantSlug: slug})
		require.NoError(t, err)
		require.True(t, res.MFARequired)
		return res.MFASessionToken
	}

	code, err := totp.GenerateCode(key.Secret(), time.Now())
	require.NoError(t, err)

	created, err := svc.VerifyMFA(ctx, login(), code, "198.51.100.5", "ua")
	require.NoError(t, err, "first use of a fresh code succeeds")
	require.NotEmpty(t, created.Token)

	_, err = svc.VerifyMFA(ctx, login(), code, "198.51.100.6", "ua")
	require.ErrorIs(t, err, vdmserr.ErrUnauthorized, "the same TOTP code must not complete a second login")

	// Disabling MFA with the burnt code is refused too.
	require.Error(t, svc.DisableMFA(ctx, tenant, userID, code, ""))
}
