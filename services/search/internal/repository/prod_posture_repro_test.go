//go:build integration && prodposture
// +build integration,prodposture

// Wave A.1.a acceptance suite for issue #70 (STATE 2026-07-03: the
// saved-search repository ran entirely on the raw pool, so under the
// prod NOBYPASSRLS posture every read failed closed and alerts never
// fired). Originally this file was the failing repro pinning the bug;
// it is now the permanent regression guard: every repository method
// must work through database.WithTenantTx under a dms_app NOBYPASSRLS
// role, and must never see (or touch) another tenant's rows.
//
// Run via the prod-posture lane:
//
//	go test -tags "integration prodposture" -run '^TestProdPosture_' ./services/search/...
package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/search/internal/model"
	"github.com/aieera/sedoc/services/search/internal/repository"
)

func TestProdPosture_SearchSavedSearchRepo(t *testing.T) {
	testutil.AssertProdPosture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	t.Cleanup(cancel)

	db := testutil.NewProdPostureDB(ctx, t, "../../migrations")
	repo := repository.New(db.App) // exactly as cmd/server/main.go wires it

	tenantA := uuid.Must(uuid.NewV7()).String()
	tenantB := uuid.Must(uuid.NewV7()).String()
	ownerA := uuid.Must(uuid.NewV7()).String()
	otherA := uuid.Must(uuid.NewV7()).String()
	userB := uuid.Must(uuid.NewV7()).String()

	newSS := func(tenant, user, name string) *model.SavedSearch {
		return &model.SavedSearch{
			ID:                    repository.NewID(),
			TenantID:              tenant,
			UserID:                user,
			Name:                  name,
			Query:                 "contract renewal",
			Notify:                true,
			NotifyIntervalMinutes: 15,
			CreatedAt:             time.Now().UTC(),
		}
	}

	ssA := newSS(tenantA, ownerA, "A alerts")

	t.Run("CRUD", func(t *testing.T) {
		require.NoError(t, repo.CreateSavedSearch(ctx, ssA),
			"create must pass the RLS WITH CHECK under NOBYPASSRLS (tenant context via WithTenantTx)")

		got, err := repo.GetSavedSearch(ctx, tenantA, ownerA, ssA.ID)
		require.NoError(t, err)
		require.Equal(t, ssA.Name, got.Name)

		list, err := repo.ListSavedSearches(ctx, tenantA, ownerA)
		require.NoError(t, err)
		require.Len(t, list, 1)

		newName := "A alerts (renamed)"
		require.NoError(t, repo.UpdateSavedSearch(ctx, tenantA, ownerA, ssA.ID,
			repository.SavedSearchPatch{Name: &newName}))
		got, err = repo.GetSavedSearch(ctx, tenantA, ownerA, ssA.ID)
		require.NoError(t, err)
		require.Equal(t, newName, got.Name)
	})

	t.Run("Subscribers", func(t *testing.T) {
		require.NoError(t, repo.AddSubscriber(ctx, tenantA, ssA.ID, otherA, ownerA, []string{"in_app", "email"}))

		subs, err := repo.ListSubscribers(ctx, tenantA, ssA.ID)
		require.NoError(t, err)
		require.Len(t, subs, 1, "subscriber match must see the row under NOBYPASSRLS")
		require.Equal(t, otherA, subs[0].UserID)

		require.NoError(t, repo.RemoveSubscriber(ctx, tenantA, ssA.ID, otherA))
		subs, err = repo.ListSubscribers(ctx, tenantA, ssA.ID)
		require.NoError(t, err)
		require.Empty(t, subs)

		// Re-add for the isolation checks below.
		require.NoError(t, repo.AddSubscriber(ctx, tenantA, ssA.ID, otherA, ownerA, []string{"in_app"}))
	})

	t.Run("SmartFolders", func(t *testing.T) {
		sf, err := repo.PromoteSmartFolder(ctx, tenantA, ownerA, ssA.ID, "public", "star", nil)
		require.NoError(t, err)
		require.True(t, sf.IsSmartFolder)

		folders, err := repo.ListSmartFolders(ctx, tenantA, otherA, nil)
		require.NoError(t, err)
		require.Len(t, folders, 1, "public smart folder must be visible tenant-wide under NOBYPASSRLS")

		got, err := repo.GetSmartFolder(ctx, tenantA, ssA.ID)
		require.NoError(t, err)
		require.Equal(t, "star", got.Icon)

		require.NoError(t, repo.DemoteSmartFolder(ctx, tenantA, ownerA, ssA.ID))
	})

	t.Run("CrossTenantIsolation", func(t *testing.T) {
		// Tenant B gets its own row so B's context is provably live.
		ssB := newSS(tenantB, userB, "B alerts")
		require.NoError(t, repo.CreateSavedSearch(ctx, ssB))

		// B must not read A's rows — not by list, not by id, not via
		// subscribers, not via smart folders.
		list, err := repo.ListSavedSearches(ctx, tenantB, ownerA)
		require.NoError(t, err)
		require.Empty(t, list)

		_, err = repo.GetSavedSearch(ctx, tenantB, userB, ssA.ID)
		require.ErrorIs(t, err, vdmserr.ErrNotFound)

		subs, err := repo.ListSubscribers(ctx, tenantB, ssA.ID)
		require.NoError(t, err)
		require.Empty(t, subs, "tenant B must not see tenant A's subscribers")

		folders, err := repo.ListSmartFolders(ctx, tenantB, userB, nil)
		require.NoError(t, err)
		require.Empty(t, folders)

		// B must not be able to write to A's rows either.
		require.ErrorIs(t, repo.DeleteSavedSearch(ctx, tenantB, userB, ssA.ID), vdmserr.ErrNotFound)
		nm := "hijack"
		require.ErrorIs(t, repo.UpdateSavedSearch(ctx, tenantB, userB, ssA.ID,
			repository.SavedSearchPatch{Name: &nm}), vdmserr.ErrNotFound)

		// A's row is intact.
		_, err = repo.GetSavedSearch(ctx, tenantA, ownerA, ssA.ID)
		require.NoError(t, err)
	})

	t.Run("Delete", func(t *testing.T) {
		require.NoError(t, repo.DeleteAllSubscribers(ctx, tenantA, ssA.ID))
		require.NoError(t, repo.DeleteSavedSearch(ctx, tenantA, ownerA, ssA.ID))
		_, err := repo.GetSavedSearch(ctx, tenantA, ownerA, ssA.ID)
		require.ErrorIs(t, err, vdmserr.ErrNotFound)
	})
}
