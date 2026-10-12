//go:build integration

package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/notification/internal/model"
	"github.com/aieera/sedoc/services/notification/internal/repository"
)

// GET /api/v1/notifications?limit=N built "LIMIT " + itoa(N), and itoa
// only handled two digits: limit=100 (the documented maximum) rendered
// "LIMIT :0", a syntax error, so the inbox 500'd. The limit must be a
// bound parameter and must actually cap the result.
func TestListNotifications_LimitApplied(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dsn, cleanup, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	migrateNotificationSchema(t, dsn)
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	tenant, user := seedTenantUser(ctx, t, pool)
	repo := repository.New(pool)
	for i := 0; i < 5; i++ {
		require.NoError(t, repo.Insert(ctx, &model.Notification{
			TenantID: tenant.String(), UserID: user.String(), Type: "t", Title: "n",
			ResourceType: "document", ResourceID: uuid.NewString(),
			Channel: model.ChannelInApp, CreatedAt: time.Now().UTC(),
		}))
	}
	for _, tc := range []struct{ limit, want int }{{3, 3}, {100, 5}, {0, 5}, {1000, 5}} {
		got, err := repo.List(ctx, tenant.String(), user.String(), nil, tc.limit)
		require.NoError(t, err, "limit=%d", tc.limit)
		require.Len(t, got, tc.want, "limit=%d", tc.limit)
	}
}
