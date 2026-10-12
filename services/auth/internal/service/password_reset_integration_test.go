//go:build integration

// N-1 (reset-token half) — the dms.auth.password_reset_requested.v1 outbox
// payload carried the plaintext reset token, and the connector's webhook
// fan-out forwarded dms.auth.> verbatim: any member could subscribe an
// external URL and take over an owner account. The event now carries only
// an opaque reset_ref; the token sits in Redis at pwreset:ref:<reset_ref>
// for the token's lifetime, where the notification service GETDELs it to
// build the email link.
package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/services/auth/internal/testenv"
)

func TestPasswordResetEvent_HasNoToken(t *testing.T) {
	env := integrationEnv(t)
	ctx := testCtx(t)
	svc := newIntegrationService(t, env)

	slug := testenv.UniqueSlug("reset")
	tenant := testenv.SeedTenant(ctx, t, env.Pool, slug)
	email := "reset-" + uuid.NewString()[:8] + "@example.com"
	testenv.SeedUser(ctx, t, env.Pool, tenant, testenv.UserSpec{Email: email})

	svc.RequestPasswordReset(ctx, email, slug, "198.51.100.8")

	var raw []byte
	require.NoError(t, env.Pool.QueryRow(ctx, `
		SELECT payload FROM outbox
		 WHERE tenant_id = $1 AND event_type = 'dms.auth.password_reset_requested.v1'
		 ORDER BY created_at DESC LIMIT 1`, tenant).Scan(&raw))
	var payload map[string]any
	require.NoError(t, json.Unmarshal(raw, &payload))

	require.NotContains(t, payload, "reset_token", "the outbox event must not carry the reset token")
	for k, v := range payload {
		if s, ok := v.(string); ok {
			require.NotContains(t, s, tenant.String()+".", "no field may embed the token (%s)", k)
		}
	}
	ref, _ := payload["reset_ref"].(string)
	_, err := uuid.Parse(ref)
	require.NoError(t, err, "reset_ref must be a uuid, got %q", ref)

	key := "pwreset:ref:" + ref
	token, err := env.Redis.Get(ctx, key).Result()
	require.NoError(t, err, "the token must be parked in Redis under the reference")
	ttl, err := env.Redis.TTL(ctx, key).Result()
	require.NoError(t, err)
	require.Greater(t, ttl, 25*time.Minute)
	require.LessOrEqual(t, ttl, resetTokenTTL)

	sum := sha256.Sum256([]byte(token))
	var n int
	require.NoError(t, env.Pool.QueryRow(ctx,
		`SELECT count(*) FROM password_reset_tokens WHERE tenant_id = $1 AND token_hash = $2`,
		tenant, hex.EncodeToString(sum[:])).Scan(&n))
	require.Equal(t, 1, n, "the parked token is the one whose hash was stored")

	// The token from the reference completes a reset.
	require.NoError(t, svc.ResetPassword(ctx, token, "Brand-New-Passw0rd!"))
}
