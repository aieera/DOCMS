//go:build integration

// H-6 — an LDAP-authenticated login must go through the same tenant MFA
// policy block as a local-password login. Before the fix the LDAP success
// branch minted a full session immediately, so `required` MFA and a
// user's own TOTP were skipped for every directory-backed account.
package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/services/auth/internal/model"
	"github.com/aieera/sedoc/services/auth/internal/repository"
	"github.com/aieera/sedoc/services/auth/internal/testenv"
)

// activeLDAPRepo reports an active, no-fallback LDAP config for any tenant.
// Only GetActiveConfig is reachable from Login.
type activeLDAPRepo struct{ repository.LDAPRepository }

func (activeLDAPRepo) GetActiveConfig(_ context.Context, _ pgx.Tx, tenantID uuid.UUID) (*repository.LDAPConfigRow, error) {
	return &repository.LDAPConfigRow{TenantID: tenantID, ID: uuid.New(), IsActive: true}, nil
}

// ldapLoginFixture: tenant with an active LDAP config whose directory
// accepts the user (the authenticator returns the local user row).
func ldapLoginFixture(t *testing.T, policy string, mfaEnabled bool) (*Service, string, string) {
	t.Helper()
	env := integrationEnv(t)
	ctx := testCtx(t)
	slug := testenv.UniqueSlug("ldap")
	tenant := testenv.SeedTenant(ctx, t, env.Pool, slug)
	email := "dir-" + uuid.NewString()[:8] + "@example.com"
	userID := testenv.SeedUser(ctx, t, env.Pool, tenant, testenv.UserSpec{
		Email: email, MFAEnabled: mfaEnabled, MFASecretEnc: map[bool]string{true: "sealed-secret"}[mfaEnabled],
	})
	if policy != "" {
		setMFAPolicy(t, env, tenant, policy)
	}

	svc := newIntegrationService(t, env)
	svc.SetLDAP(LDAPDeps{Repo: activeLDAPRepo{}})
	svc.ldapAuthn = func(ctx context.Context, tid uuid.UUID, _, _ string) (*LDAPLoginResult, error) {
		u, err := svc.loadUser(ctx, tid, userID)
		if err != nil {
			return nil, err
		}
		return &LDAPLoginResult{User: u}, nil
	}
	return svc, email, slug
}

func TestLDAPLogin_RequiredPolicyChallenges(t *testing.T) {
	svc, email, slug := ldapLoginFixture(t, "required", true)
	res, err := svc.Login(testCtx(t), LoginInput{Email: email, Password: "directory-password", TenantSlug: slug})
	require.NoError(t, err)
	require.True(t, res.MFARequired, "required MFA policy must challenge an LDAP login")
	require.Nil(t, res.Session, "no session may be issued before the second factor")
	require.NotEmpty(t, res.MFASessionToken)
}

func TestLDAPLogin_RequiredPolicyWithoutFactorRefused(t *testing.T) {
	svc, email, slug := ldapLoginFixture(t, "required", false)
	_, err := svc.Login(testCtx(t), LoginInput{Email: email, Password: "directory-password", TenantSlug: slug})
	require.ErrorIs(t, err, ErrMFAEnrollmentRequired)
}

func TestLDAPLogin_UserTOTPChallenges(t *testing.T) {
	svc, email, slug := ldapLoginFixture(t, "", true) // default optional policy
	res, err := svc.Login(testCtx(t), LoginInput{Email: email, Password: "directory-password", TenantSlug: slug})
	require.NoError(t, err)
	require.True(t, res.MFARequired, "a user's own TOTP must be challenged on an LDAP login")
}

func TestLDAPLogin_NoMFAIssuesSession(t *testing.T) {
	svc, email, slug := ldapLoginFixture(t, "", false)
	res, err := svc.Login(testCtx(t), LoginInput{Email: email, Password: "directory-password", TenantSlug: slug})
	require.NoError(t, err)
	require.False(t, res.MFARequired)
	require.NotNil(t, res.Session)
	require.Equal(t, model.Role("member"), res.Session.UserView.Role)
}
