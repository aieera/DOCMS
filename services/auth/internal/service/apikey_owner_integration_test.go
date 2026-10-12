//go:build integration

// M-8 (API keys) — a key whose owning user is suspended, deactivated or
// deleted must stop authenticating. SuspendUser revokes keys, but other
// status paths (SCIM deactivate/PATCH, direct admin edits, soft delete)
// did not, and the pre-tenant lookup (auth_lookup_api_key_by_hash) never
// looked at the owner. The lookup now returns only keys whose owner is
// active (or that have no owner).
package service

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/auth/internal/testenv"
)

func TestAPIKey_SuspendedOwnerRejected(t *testing.T) {
	env := integrationEnv(t)
	ctx := testCtx(t)
	svc := newIntegrationService(t, env)
	tenant := testenv.SeedTenant(ctx, t, env.Pool, testenv.UniqueSlug("keyowner"))

	for _, change := range []struct{ name, sql string }{
		{"suspended", `UPDATE users SET status = 'suspended' WHERE tenant_id = $1 AND id = $2`},
		{"deactivated", `UPDATE users SET status = 'deactivated' WHERE tenant_id = $1 AND id = $2`},
		{"deleted", `UPDATE users SET deleted_at = now() WHERE tenant_id = $1 AND id = $2`},
	} {
		t.Run(change.name, func(t *testing.T) {
			user := testenv.SeedUser(ctx, t, env.Pool, tenant, testenv.UserSpec{
				Email: change.name + "-" + uuid.NewString()[:8] + "@example.com", Role: "admin",
			})
			issued, err := svc.IssueAPIKey(ctx, IssueAPIKeyInput{
				TenantID: tenant, UserID: user, Name: "k", Scopes: []string{"documents:read"},
			})
			require.NoError(t, err)
			_, err = svc.ValidateAPIKey(ctx, issued.Key, "")
			require.NoError(t, err, "active owner: key works")

			_, err = env.Pool.Exec(ctx, change.sql, tenant, user)
			require.NoError(t, err)
			_, err = svc.ValidateAPIKey(ctx, issued.Key, "")
			require.ErrorIs(t, err, vdmserr.ErrUnauthorized, "a %s owner's key must not authenticate", change.name)
		})
	}

	t.Run("ownerless key unaffected", func(t *testing.T) {
		const plain = "vdms_OwnerlessKey00000000000000000000000000000000000"
		_, err := env.Pool.Exec(ctx, `
			INSERT INTO api_keys (tenant_id, name, key_hash, key_prefix, scopes)
			VALUES ($1, 'service', $2, $3, '{documents:read}')`,
			tenant, sha256Hex(plain), plain[:APIKeyPrefixLen])
		require.NoError(t, err)
		_, err = svc.ValidateAPIKey(ctx, plain, "")
		require.NoError(t, err)
	})
}
