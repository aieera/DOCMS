//go:build integration

// DEF-01 — "Regenerate recovery codes" used to re-run SetupMFA, which
// minted a NEW TOTP secret while mfa_enabled stayed true: the user's
// authenticator app stopped working on the spot and sign-in had no
// recovery-code entry, so the user was locked out. Regeneration is now its
// own operation: it proves possession of the CURRENT authenticator (a
// replay-protected TOTP code), replaces only the recovery-code hashes and
// leaves the secret alone. SetupMFA refuses to run while MFA is on.
package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/auth/internal/repository"
	"github.com/aieera/sedoc/services/auth/internal/testenv"
)

type mfaRow struct {
	enabled bool
	secret  string
	hashes  []string
}

func readMFARow(t *testing.T, tenant, user uuid.UUID) mfaRow {
	t.Helper()
	var r mfaRow
	var secret *string
	require.NoError(t, integrationEnv(t).Pool.QueryRow(context.Background(), `
		SELECT mfa_enabled, mfa_secret_encrypted, COALESCE(mfa_recovery_hashes, '{}')
		  FROM users WHERE tenant_id = $1 AND id = $2`, tenant, user).
		Scan(&r.enabled, &secret, &r.hashes))
	if secret != nil {
		r.secret = *secret
	}
	return r
}

// seedTOTPUser seeds a user with TOTP enrolled (enabled + sealed secret +
// a known set of recovery codes) and returns the plaintext secret and the
// old recovery codes.
func seedTOTPUser(t *testing.T, svc *Service) (tenant, user uuid.UUID, secret string, oldCodes []string) {
	t.Helper()
	env := integrationEnv(t)
	ctx := testCtx(t)
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "SeDoc", AccountName: "regen@example.com"})
	require.NoError(t, err)
	sealed, err := svc.encryptMFASecret(key.Secret())
	require.NoError(t, err)

	tenant = testenv.SeedTenant(ctx, t, env.Pool, testenv.UniqueSlug("regen"))
	user = testenv.SeedUser(ctx, t, env.Pool, tenant, testenv.UserSpec{
		Email:      "regen-" + uuid.NewString()[:8] + "@example.com",
		MFAEnabled: true, MFASecretEnc: sealed,
	})
	// Cheap (MinCost) hashes for the seeded codes: bcrypt-12 under -race
	// costs seconds per code and the seed is not what is under test.
	oldCodes = []string{"AAAAAAAA", "BBBBBBBB", "CCCCCCCC", "DDDDDDDD", "EEEEEEEE", "FFFFFFFF", "GGGGGGGG", "HHHHHHHH"}
	oldHashes := make([]string, len(oldCodes))
	for i, c := range oldCodes {
		h, hashErr := bcrypt.GenerateFromPassword([]byte(c), bcrypt.MinCost)
		require.NoError(t, hashErr)
		oldHashes[i] = string(h)
	}
	_, err = env.Pool.Exec(ctx, `UPDATE users SET mfa_recovery_hashes = $3 WHERE tenant_id = $1 AND id = $2`,
		tenant, user, oldHashes)
	require.NoError(t, err)
	return tenant, user, key.Secret(), oldCodes
}

func currentCode(t *testing.T, secret string) string {
	t.Helper()
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	return code
}

func TestRegenerateRecoveryCodes_ReplacesCodesKeepsSecret(t *testing.T) {
	svc := newIntegrationService(t, integrationEnv(t))
	ctx := testCtx(t)
	tenant, user, secret, oldCodes := seedTOTPUser(t, svc)
	before := readMFARow(t, tenant, user)

	codes, err := svc.RegenerateRecoveryCodes(ctx, tenant, user, currentCode(t, secret))
	require.NoError(t, err)
	require.Len(t, codes, 8)

	after := readMFARow(t, tenant, user)
	require.True(t, after.enabled, "MFA must stay on")
	require.Equal(t, before.secret, after.secret, "the TOTP secret must NOT change — that was the lockout")
	require.Len(t, after.hashes, 8)
	require.NotEqual(t, before.hashes, after.hashes)
	for _, c := range codes {
		require.NotEmpty(t, consumeRecoveryCode(after.hashes, c), "new code %s must verify", c)
		require.NotContains(t, after.hashes, c, "only hashes are stored")
	}
	for _, c := range oldCodes {
		require.Empty(t, consumeRecoveryCode(after.hashes, c), "old code %s must be invalid", c)
	}

	// The authenticator app keeps working: the stored secret still decrypts
	// to the enrolled one.
	got, err := svc.decryptMFASecret(after.secret)
	require.NoError(t, err)
	require.Equal(t, secret, got)

	// Recorded like the other factor changes (dms.user.mfa_changed.v1).
	var raw []byte
	require.NoError(t, integrationEnv(t).Pool.QueryRow(ctx, `
		SELECT payload FROM outbox
		 WHERE tenant_id = $1 AND event_type = 'dms.user.mfa_changed.v1'
		 ORDER BY created_at DESC LIMIT 1`, tenant).Scan(&raw))
	var payload map[string]any
	require.NoError(t, json.Unmarshal(raw, &payload))
	require.Equal(t, "recovery_codes_regenerated", payload["action"])
	require.Equal(t, "totp", payload["method"])
	require.Equal(t, user.String(), payload["user_id"])
	for k, v := range payload {
		if s, ok := v.(string); ok {
			for _, c := range codes {
				require.NotContains(t, s, c, "event field %s must not carry a recovery code", k)
			}
		}
	}
}

func TestRegenerateRecoveryCodes_WrongCode(t *testing.T) {
	svc := newIntegrationService(t, integrationEnv(t))
	ctx := testCtx(t)
	tenant, user, secret, _ := seedTOTPUser(t, svc)
	before := readMFARow(t, tenant, user)

	wrong := "000000"
	if currentCode(t, secret) == wrong {
		wrong = "111111"
	}
	_, err := svc.RegenerateRecoveryCodes(ctx, tenant, user, wrong)
	require.Error(t, err)
	require.Equal(t, vdmserr.KindValidation, vdmserr.KindOf(err))
	var e *vdmserr.Error
	require.ErrorAs(t, err, &e)
	require.Equal(t, "totp_code", e.Field)
	require.Equal(t, before, readMFARow(t, tenant, user), "a wrong code changes nothing")
}

func TestRegenerateRecoveryCodes_MFAOff(t *testing.T) {
	env := integrationEnv(t)
	svc := newIntegrationService(t, env)
	ctx := testCtx(t)
	tenant := testenv.SeedTenant(ctx, t, env.Pool, testenv.UniqueSlug("regenoff"))
	user := testenv.SeedUser(ctx, t, env.Pool, tenant, testenv.UserSpec{
		Email: "regenoff-" + uuid.NewString()[:8] + "@example.com",
	})

	_, err := svc.RegenerateRecoveryCodes(ctx, tenant, user, "123456")
	require.Error(t, err)
	require.Equal(t, vdmserr.KindConflict, vdmserr.KindOf(err))
	require.Empty(t, readMFARow(t, tenant, user).hashes)
}

// Pending enrolment (setup ran, confirm did not): a secret exists but MFA is
// off. Regeneration is for enabled MFA only.
func TestRegenerateRecoveryCodes_PendingSetupIsConflict(t *testing.T) {
	env := integrationEnv(t)
	svc := newIntegrationService(t, env)
	ctx := testCtx(t)
	tenant := testenv.SeedTenant(ctx, t, env.Pool, testenv.UniqueSlug("regenpend"))
	user := testenv.SeedUser(ctx, t, env.Pool, tenant, testenv.UserSpec{
		Email: "regenpend-" + uuid.NewString()[:8] + "@example.com",
	})
	setup, err := svc.SetupMFA(ctx, tenant, user, "regenpend@example.com")
	require.NoError(t, err)

	_, err = svc.RegenerateRecoveryCodes(ctx, tenant, user, currentCode(t, setup.Secret))
	require.Error(t, err)
	require.Equal(t, vdmserr.KindConflict, vdmserr.KindOf(err))
}

func TestRegenerateRecoveryCodes_ReplayedCodeRejected(t *testing.T) {
	svc := newIntegrationService(t, integrationEnv(t))
	ctx := testCtx(t)
	tenant, user, secret, _ := seedTOTPUser(t, svc)

	code := currentCode(t, secret)
	first, err := svc.RegenerateRecoveryCodes(ctx, tenant, user, code)
	require.NoError(t, err)
	afterFirst := readMFARow(t, tenant, user)

	_, err = svc.RegenerateRecoveryCodes(ctx, tenant, user, code)
	require.Error(t, err, "a TOTP code is single-use")
	require.Equal(t, vdmserr.KindValidation, vdmserr.KindOf(err))
	afterReplay := readMFARow(t, tenant, user)
	require.Equal(t, afterFirst, afterReplay, "a replayed code changes nothing")
	require.NotEmpty(t, consumeRecoveryCode(afterReplay.hashes, first[0]))
}

func TestSetupMFA_RefusedWhileEnabled(t *testing.T) {
	svc := newIntegrationService(t, integrationEnv(t))
	ctx := testCtx(t)
	tenant, user, secret, _ := seedTOTPUser(t, svc)
	before := readMFARow(t, tenant, user)

	_, err := svc.SetupMFA(ctx, tenant, user, "regen@example.com")
	require.Error(t, err)
	require.Equal(t, vdmserr.KindConflict, vdmserr.KindOf(err))
	require.Contains(t, err.Error(), "turn it off first")

	after := readMFARow(t, tenant, user)
	require.Equal(t, before, after, "a refused setup must not touch the secret or the codes")
	got, err := svc.decryptMFASecret(after.secret)
	require.NoError(t, err)
	require.Equal(t, secret, got)
}

// Re-running setup BEFORE confirming (e.g. the user reloads the QR page)
// still works: only an enabled factor is protected.
func TestSetupMFA_RerunWhilePendingStillAllowed(t *testing.T) {
	env := integrationEnv(t)
	svc := newIntegrationService(t, env)
	ctx := testCtx(t)
	tenant := testenv.SeedTenant(ctx, t, env.Pool, testenv.UniqueSlug("setuppend"))
	user := testenv.SeedUser(ctx, t, env.Pool, tenant, testenv.UserSpec{
		Email: "setuppend-" + uuid.NewString()[:8] + "@example.com",
	})
	first, err := svc.SetupMFA(ctx, tenant, user, "setuppend@example.com")
	require.NoError(t, err)
	second, err := svc.SetupMFA(ctx, tenant, user, "setuppend@example.com")
	require.NoError(t, err)
	require.NotEqual(t, first.Secret, second.Secret)
}

// The enabled-check and the write are separate statements, so the write
// itself must refuse an enabled factor: a confirm that commits between
// SetupMFA's check and its write (two tabs) must not get its secret
// replaced.
func TestSetMFASecret_RefusesEnabledFactor(t *testing.T) {
	env := integrationEnv(t)
	svc := newIntegrationService(t, env)
	ctx := testCtx(t)
	tenant, user, _, _ := seedTOTPUser(t, svc)
	before := readMFARow(t, tenant, user)

	err := database.WithTenantTx(ctx, env.Pool, tenant, func(tx pgx.Tx) error {
		return svc.users.SetMFASecret(ctx, tx, tenant, user, "replacement", []string{"x"})
	})
	require.ErrorIs(t, err, repository.ErrMFAEnabled)
	require.Equal(t, before, readMFARow(t, tenant, user))
}
