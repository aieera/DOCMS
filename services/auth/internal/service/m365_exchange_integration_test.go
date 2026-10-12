//go:build integration

// H-7 / DEC-15 — the M365 add-in exchange maps an Entra identity to a
// SeDoc user. The token's signature, aud and tid are verified, but the
// `email` claim's content is not: Entra lets it be set to any address
// unless `xms_edov` says the domain is verified. The exchange therefore
// maps by UPN (preferred_username), trusts email only with xms_edov,
// refuses non-active users, and honours a `required` tenant MFA policy
// by demanding `amr` contain "mfa".
package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/auth/internal/testenv"
)

// fixedEntra is a verifier that "verifies" every token as one identity.
type fixedEntra struct{ id *VerifiedEntraIdentity }

func (f fixedEntra) Verify(context.Context, string) (*VerifiedEntraIdentity, error) {
	cp := *f.id
	return &cp, nil
}

type m365Fixture struct {
	svc    *Service
	tenant uuid.UUID
}

func newM365Fixture(t *testing.T, id *VerifiedEntraIdentity) *m365Fixture {
	t.Helper()
	env := integrationEnv(t)
	tenant := testenv.SeedTenant(testCtx(t), t, env.Pool, testenv.UniqueSlug("m365"))
	svc := newIntegrationService(t, env)
	svc.m365 = fixedEntra{id: id}
	return &m365Fixture{svc: svc, tenant: tenant}
}

func (f *m365Fixture) user(t *testing.T, email, status string) uuid.UUID {
	t.Helper()
	return testenv.SeedUser(testCtx(t), t, integrationEnv(t).Pool, f.tenant, testenv.UserSpec{Email: email, Status: status})
}

func uniqueAddr(local, domain string) string {
	return local + "-" + uuid.NewString()[:8] + "@" + domain
}

func TestM365Exchange_UnverifiedEmailIgnored(t *testing.T) {
	victim := uniqueAddr("ceo", "corp.example")
	f := newM365Fixture(t, &VerifiedEntraIdentity{
		TID: "tid-1", OID: "oid-attacker",
		UPN:   uniqueAddr("attacker", "evil.onmicrosoft.com"),
		Email: victim, EmailVerified: false, // attacker-set, unverified
	})
	f.user(t, victim, "active")

	res, err := f.svc.ExchangeM365Token(testCtx(t), "token", "198.51.100.1", "ua", "")
	require.ErrorIs(t, err, ErrM365NoVDMSUser, "an unverified email claim must never map to a SeDoc user")
	require.Nil(t, res)
}

func TestM365Exchange_VerifiedEmailMaps(t *testing.T) {
	addr := uniqueAddr("alice", "corp.example")
	f := newM365Fixture(t, &VerifiedEntraIdentity{
		TID: "tid-1", OID: "oid-alice",
		UPN:   uniqueAddr("alice", "corp.onmicrosoft.com"),
		Email: addr, EmailVerified: true,
	})
	uid := f.user(t, addr, "active")

	res, err := f.svc.ExchangeM365Token(testCtx(t), "token", "198.51.100.1", "ua", "")
	require.NoError(t, err)
	require.Equal(t, uid.String(), res.UserID)
}

func TestM365Exchange_MapsByUPN(t *testing.T) {
	upn := uniqueAddr("bob", "corp.example")
	f := newM365Fixture(t, &VerifiedEntraIdentity{TID: "tid-1", OID: "oid-bob", UPN: upn})
	uid := f.user(t, upn, "active")

	res, err := f.svc.ExchangeM365Token(testCtx(t), "token", "198.51.100.1", "ua", "")
	require.NoError(t, err)
	require.Equal(t, uid.String(), res.UserID)
	require.Equal(t, f.tenant.String(), res.TenantID)
}

func TestM365Exchange_SuspendedUser403(t *testing.T) {
	upn := uniqueAddr("carol", "corp.example")
	f := newM365Fixture(t, &VerifiedEntraIdentity{TID: "tid-1", OID: "oid-carol", UPN: upn})
	f.user(t, upn, "suspended")

	res, err := f.svc.ExchangeM365Token(testCtx(t), "token", "198.51.100.1", "ua", "")
	require.ErrorIs(t, err, ErrAccountNotActive)
	require.Equal(t, vdmserr.KindForbidden, vdmserr.KindOf(err), "a suspended user is a 403")
	require.Nil(t, res)
}

func TestM365Exchange_RequiredMFAWithoutAmr403(t *testing.T) {
	upn := uniqueAddr("dave", "corp.example")
	f := newM365Fixture(t, &VerifiedEntraIdentity{TID: "tid-1", OID: "oid-dave", UPN: upn, AMR: []string{"pwd"}})
	f.user(t, upn, "active")
	setMFAPolicy(t, integrationEnv(t), f.tenant, "required")

	res, err := f.svc.ExchangeM365Token(testCtx(t), "token", "198.51.100.1", "ua", "")
	require.ErrorIs(t, err, ErrExchangeMFARequired)
	require.Equal(t, vdmserr.KindForbidden, vdmserr.KindOf(err))
	require.Nil(t, res)

	// The same identity with an MFA-backed Entra sign-in passes.
	f.svc.m365 = fixedEntra{id: &VerifiedEntraIdentity{TID: "tid-1", OID: "oid-dave", UPN: upn, AMR: []string{"pwd", "mfa"}}}
	res, err = f.svc.ExchangeM365Token(testCtx(t), "token", "198.51.100.1", "ua", "")
	require.NoError(t, err)
	require.NotEmpty(t, res.SessionToken)
}
