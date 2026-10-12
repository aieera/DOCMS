//go:build integration && prodposture

// M-27 inventory — background and best-effort writes the auth service ran
// on the raw pool, all silent no-ops under the production dms_app
// (NOBYPASSRLS) role:
//   - the 15-minute LDAP sync scheduler listed active ldap_configs
//     (FORCE RLS) with no tenant set → found no tenant, never synced;
//   - the API-key last_used_at touch updated api_keys (FORCE RLS) with no
//     tenant set → matched no row.
package service

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/auth/internal/repository"
	"github.com/aieera/sedoc/services/auth/internal/testenv"
)

func TestProdPosture_LDAPSchedulerFindsTenants(t *testing.T) {
	testutil.AssertProdPosture(t)
	ctx, db := testenv.ProdPosture(t)

	active := testenv.SeedTenant(ctx, t, db.Super, testenv.UniqueSlug("ldap-on"))
	inactive := testenv.SeedTenant(ctx, t, db.Super, testenv.UniqueSlug("ldap-off"))
	testenv.SeedTenant(ctx, t, db.Super, testenv.UniqueSlug("ldap-none"))
	for tenant, on := range map[uuid.UUID]bool{active: true, inactive: false} {
		_, err := db.Super.Exec(ctx, `
			INSERT INTO ldap_configs (tenant_id, url, bind_dn, bind_password_sealed,
			                          user_search_base, user_search_filter,
			                          group_search_base, group_search_filter, is_active)
			VALUES ($1, 'ldaps://dir.example:636', 'cn=svc', '\x00',
			        'dc=example', '(uid={username})', 'dc=example', '(member={user_dn})', $2)`,
			tenant, on)
		require.NoError(t, err)
	}
	db.App.Reset()

	svc := &Service{pool: db.App, log: zerolog.Nop(), now: time.Now}
	got, err := svc.ldapSyncTenantIDs(ctx, db.App)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{active}, got,
		"the scheduler must find exactly the tenants with an active LDAP config under NOBYPASSRLS")
}

func TestProdPosture_APIKeyLastUsedTouch(t *testing.T) {
	testutil.AssertProdPosture(t)
	ctx, db := testenv.ProdPosture(t)

	redisURL, redisClean, err := testutil.NewRedisContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(redisClean)
	opts, err := redis.ParseURL(redisURL)
	require.NoError(t, err)
	rdb := redis.NewClient(opts)
	t.Cleanup(func() { _ = rdb.Close() })

	tenant := testenv.SeedTenant(ctx, t, db.Super, testenv.UniqueSlug("apikey-touch"))
	user := testenv.SeedUser(ctx, t, db.Super, tenant, testenv.UserSpec{Email: "touch@example.com", Role: "admin"})

	svc := New(Config{
		Pool: db.App, Redis: rdb,
		Users: repository.NewUserRepo(), Sessions: repository.NewSessionRepo(),
		APIKeys: repository.NewAPIKeyRepo(), Outbox: database.NewOutboxRepository(),
		Logger: zerolog.Nop(),
	})
	issued, err := svc.IssueAPIKey(ctx, IssueAPIKeyInput{
		TenantID: tenant, UserID: user, Name: "touch", Scopes: []string{"documents:read"},
	})
	require.NoError(t, err)
	db.App.Reset()

	_, err = svc.ValidateAPIKey(ctx, issued.Key, "")
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		var lastUsed *time.Time
		if err := db.Super.QueryRow(ctx,
			`SELECT last_used_at FROM api_keys WHERE tenant_id = $1 AND id = $2`,
			tenant, issued.ID).Scan(&lastUsed); err != nil {
			return false
		}
		return lastUsed != nil
	}, 10*time.Second, 100*time.Millisecond, "last_used_at must be recorded under NOBYPASSRLS")
}
