//go:build integration

// Client-only gate (addendum W list): the web UI stops admins from making
// anyone an owner and from touching owners, but ChangeUserRole only
// guarded the last owner and self-changes — any admin could promote an
// accomplice (or a second account) to owner, or demote an owner.
package service

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/auth/internal/testenv"
)

func TestChangeRole_LastOwnerProtected(t *testing.T) {
	env := integrationEnv(t)
	ctx := testCtx(t)
	svc := newIntegrationService(t, env)
	tenant := testenv.SeedTenant(ctx, t, env.Pool, testenv.UniqueSlug("roles"))
	seed := func(role string) uuid.UUID {
		return testenv.SeedUser(ctx, t, env.Pool, tenant, testenv.UserSpec{Email: role + "-" + uuid.NewString()[:8] + "@example.com", Role: role})
	}
	owner := seed("owner")
	admin := seed("admin")
	member := seed("member")

	t.Run("admin cannot grant owner", func(t *testing.T) {
		err := svc.ChangeUserRole(ctx, tenant, admin, "admin", member, "owner")
		require.Equal(t, vdmserr.KindForbidden, vdmserr.KindOf(err), "got %v", err)
	})
	t.Run("admin cannot change an owner", func(t *testing.T) {
		err := svc.ChangeUserRole(ctx, tenant, admin, "admin", owner, "member")
		require.Equal(t, vdmserr.KindForbidden, vdmserr.KindOf(err), "got %v", err)
	})
	t.Run("admin manages non-owners", func(t *testing.T) {
		require.NoError(t, svc.ChangeUserRole(ctx, tenant, admin, "admin", member, "admin"))
		require.NoError(t, svc.ChangeUserRole(ctx, tenant, admin, "admin", member, "member"))
	})
	t.Run("owners hand over; the last owner stays", func(t *testing.T) {
		other := seed("admin")
		require.NoError(t, svc.ChangeUserRole(ctx, tenant, owner, "owner", other, "owner"), "owner may grant owner")
		require.NoError(t, svc.ChangeUserRole(ctx, tenant, other, "owner", owner, "member"), "with two owners one may be demoted")
		// The demoted ex-owner can no longer touch the remaining (last) owner,
		// and nobody can change their own role.
		err := svc.ChangeUserRole(ctx, tenant, owner, "member", other, "member")
		require.Equal(t, vdmserr.KindForbidden, vdmserr.KindOf(err), "got %v", err)
		require.Error(t, svc.ChangeUserRole(ctx, tenant, other, "owner", other, "member"), "no self-demotion of the last owner")
	})
}
