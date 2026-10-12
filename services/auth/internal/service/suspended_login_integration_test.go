//go:build integration

// M-8 — a suspended (or deactivated) user must not mint a new session on
// any path that bypasses the password-login status check: passkey login
// and the Google add-on exchange. Before the fix both issued a full
// session, which every other service then honoured for its TTL.
package service

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"

	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/auth/internal/model"
	"github.com/aieera/sedoc/services/auth/internal/testenv"
)

const (
	testRPID   = "localhost"
	testOrigin = "https://localhost"
)

// passkeyFixture is a user with one registered ES256 passkey whose private
// key the test holds, so it can produce real assertions.
type passkeyFixture struct {
	svc    *Service
	slug   string
	email  string
	tenant uuid.UUID
	user   uuid.UUID
	credID []byte
	key    *ecdsa.PrivateKey
}

func newPasskeyFixture(t *testing.T) *passkeyFixture {
	t.Helper()
	t.Setenv("SEDOC_WEBAUTHN_HMAC_SECRET", "test-webauthn-hmac-secret-0123456789")
	env := integrationEnv(t)
	ctx := testCtx(t)

	svc := newIntegrationService(t, env)
	wa, err := NewWebAuthnLib(&WebAuthnConfig{RPID: testRPID, RPDisplayName: "SeDoc Test", RPOrigins: []string{testOrigin}})
	require.NoError(t, err)
	svc.WebAuthnLib = wa

	slug := testenv.UniqueSlug("passkey")
	tenant := testenv.SeedTenant(ctx, t, env.Pool, slug)
	email := "pk-" + uuid.NewString()[:8] + "@example.com"
	user := testenv.SeedUser(ctx, t, env.Pool, tenant, testenv.UserSpec{Email: email})

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	cose, err := webauthncbor.Marshal(webauthncose.EC2PublicKeyData{
		PublicKeyData: webauthncose.PublicKeyData{KeyType: int64(webauthncose.EllipticKey), Algorithm: int64(webauthncose.AlgES256)},
		Curve:         1, // P-256
		XCoord:        key.PublicKey.X.FillBytes(make([]byte, 32)),
		YCoord:        key.PublicKey.Y.FillBytes(make([]byte, 32)),
	})
	require.NoError(t, err)
	credID := make([]byte, 16)
	_, _ = rand.Read(credID)
	require.NoError(t, database.WithTenantTx(ctx, env.Pool, tenant, func(tx pgx.Tx) error {
		return svc.webauthn.UpsertCredential(ctx, tx, &model.WebAuthnCredential{
			TenantID: tenant, UserID: user, CredentialID: credID, PublicKey: cose,
			Name: "test key", Transports: []string{"internal"},
		})
	}))
	return &passkeyFixture{svc: svc, slug: slug, email: email, tenant: tenant, user: user, credID: credID, key: key}
}

// assert builds a genuine assertion for the challenge in the begin options.
func (f *passkeyFixture) assert(t *testing.T, challenge string) []byte {
	t.Helper()
	b64 := base64.RawURLEncoding.EncodeToString
	clientData, err := json.Marshal(map[string]any{
		"type": "webauthn.get", "challenge": challenge, "origin": testOrigin, "crossOrigin": false,
	})
	require.NoError(t, err)
	rpHash := sha256.Sum256([]byte(testRPID))
	authData := append(rpHash[:], 0x05) // UP | UV
	authData = binary.BigEndian.AppendUint32(authData, 1)
	cdHash := sha256.Sum256(clientData)
	digest := sha256.Sum256(append(append([]byte{}, authData...), cdHash[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, f.key, digest[:])
	require.NoError(t, err)
	userHandle, _ := f.user.MarshalBinary()
	body, err := json.Marshal(map[string]any{
		"id": b64(f.credID), "rawId": b64(f.credID), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64(clientData),
			"authenticatorData": b64(authData),
			"signature":         b64(sig),
			"userHandle":        b64(userHandle),
		},
	})
	require.NoError(t, err)
	return body
}

func (f *passkeyFixture) begin(t *testing.T) (string, string) {
	t.Helper()
	opts, token, err := f.svc.PasskeyLoginStart(testCtx(t), f.slug, f.email)
	require.NoError(t, err)
	return opts.Response.Challenge.String(), token
}

func setUserStatus(t *testing.T, tenant, user uuid.UUID, status string) {
	t.Helper()
	_, err := integrationEnv(t).Pool.Exec(context.Background(),
		`UPDATE users SET status = $3 WHERE tenant_id = $1 AND id = $2`, tenant, user, status)
	require.NoError(t, err)
}

// Control: the crafted assertion is genuinely valid for an active user.
func TestPasskeyLogin_ActiveUserGetsSession(t *testing.T) {
	f := newPasskeyFixture(t)
	challenge, token := f.begin(t)
	created, err := f.svc.PasskeyLoginFinish(testCtx(t), token, f.assert(t, challenge), "198.51.100.2", "ua")
	require.NoError(t, err)
	require.NotEmpty(t, created.Token)
}

func TestPasskeyLogin_Suspended403(t *testing.T) {
	f := newPasskeyFixture(t)
	challenge, token := f.begin(t)
	setUserStatus(t, f.tenant, f.user, "suspended") // admin suspends mid-ceremony

	created, err := f.svc.PasskeyLoginFinish(testCtx(t), token, f.assert(t, challenge), "198.51.100.2", "ua")
	require.ErrorIs(t, err, ErrAccountNotActive)
	require.Equal(t, vdmserr.KindForbidden, vdmserr.KindOf(err))
	require.Nil(t, created)
}

func TestPasskeyLogin_SuspendedCannotBegin(t *testing.T) {
	f := newPasskeyFixture(t)
	setUserStatus(t, f.tenant, f.user, "suspended")
	_, _, err := f.svc.PasskeyLoginStart(testCtx(t), f.slug, f.email)
	require.ErrorIs(t, err, ErrInvalidCredentials, "begin stays opaque: no status oracle before possession is proven")
}

// fixedGoogle "verifies" every token as one Google identity.
type fixedGoogle struct{ id VerifiedGoogleIdentity }

func (f fixedGoogle) Verify(context.Context, string) (*VerifiedGoogleIdentity, error) {
	cp := f.id
	return &cp, nil
}

func TestGoogleExchange_Suspended403(t *testing.T) {
	env := integrationEnv(t)
	ctx := testCtx(t)
	tenant := testenv.SeedTenant(ctx, t, env.Pool, testenv.UniqueSlug("google"))
	email := "g-" + uuid.NewString()[:8] + "@acme.example"
	testenv.SeedUser(ctx, t, env.Pool, tenant, testenv.UserSpec{Email: email, Status: "suspended"})

	svc := newIntegrationService(t, env)
	svc.google = fixedGoogle{id: VerifiedGoogleIdentity{Sub: "sub", Email: email, HD: "acme.example"}}
	res, err := svc.ExchangeGoogleToken(ctx, "google.id.token", "198.51.100.3", "ua", "")
	require.ErrorIs(t, err, ErrAccountNotActive)
	require.Equal(t, vdmserr.KindForbidden, vdmserr.KindOf(err))
	require.Nil(t, res)
}

// The shared session choke point refuses a non-active user no matter which
// flow (SAML, OIDC, MFA methods, ...) reaches it with a stale user value.
func TestCreateSession_RefusesNonActiveUser(t *testing.T) {
	env := integrationEnv(t)
	ctx := testCtx(t)
	tenant := testenv.SeedTenant(ctx, t, env.Pool, testenv.UniqueSlug("sess"))
	email := "s-" + uuid.NewString()[:8] + "@example.com"
	uid := testenv.SeedUser(ctx, t, env.Pool, tenant, testenv.UserSpec{Email: email, Status: "deactivated"})
	svc := newIntegrationService(t, env)

	stale := &model.User{ID: uid, TenantID: tenant, Email: email, Role: model.RoleMember, Status: model.StatusActive}
	created, err := svc.finishLogin(ctx, stale, "saml", "198.51.100.4", "ua")
	require.ErrorIs(t, err, ErrAccountNotActive)
	require.Nil(t, created)
}
