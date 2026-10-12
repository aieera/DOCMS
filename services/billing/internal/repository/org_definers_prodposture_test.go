//go:build integration && prodposture
// +build integration,prodposture

// Billing's organizations writes under the production grants (L-10).
//
// document 000001 REVOKEs INSERT/UPDATE/DELETE on organizations from
// dms_app; the only sanctioned write path is the SECURITY DEFINER
// functions of document 000111 (org_create, org_set_status,
// org_set_feature_flags). The other billing prod-posture tests grant ALL
// TABLES to dms_app, which masked the raw-pool writes; this test restores
// the real revoke first.
package repository_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/billing/internal/model"
	"github.com/aieera/sedoc/services/billing/internal/repository"
)

// setupProdOrgGrants builds the billing schema, installs document 000111's
// org-mutation definers and re-applies 000001's organizations revoke.
func setupProdOrgGrants(ctx context.Context, t *testing.T) (*testutil.ProdPostureDB, *repository.Repository) {
	t.Helper()
	db, repo := setupBillingSchema(ctx, t)
	definers, err := os.ReadFile("../../../document/migrations/000111_org_mutation_definers.up.sql")
	require.NoError(t, err)
	_, err = db.Super.Exec(ctx, string(definers))
	require.NoError(t, err, "document 000111 org definers")
	_, err = db.Super.Exec(ctx, `REVOKE INSERT, UPDATE, DELETE ON organizations FROM dms_app`)
	require.NoError(t, err)
	return db, repo
}

func TestProdPosture_CreateOrg(t *testing.T) {
	testutil.AssertProdPosture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	db, repo := setupProdOrgGrants(ctx, t)

	// Provisioning creates the organization through org_create.
	id, err := repo.CreateOrg(ctx, "Acme Holdings", "enterprise", "eu-west-1")
	require.NoError(t, err, "CreateOrg must work as dms_app under the organizations revoke")
	tenant, err := uuid.Parse(id)
	require.NoError(t, err)

	var name, slug, plan, region string
	var settings []byte
	require.NoError(t, db.Super.QueryRow(ctx,
		`SELECT name, slug, plan, primary_region, settings FROM organizations WHERE id = $1`, tenant).
		Scan(&name, &slug, &plan, &region, &settings))
	require.Equal(t, "Acme Holdings", name)
	require.Equal(t, "acme-holdings", slug)
	require.Equal(t, "enterprise", plan)
	require.Equal(t, "eu-west-1", region)
	var s struct {
		Features model.FeatureFlags `json:"features"`
	}
	require.NoError(t, json.Unmarshal(settings, &s))
	require.Equal(t, model.DefaultFlagsByPlan("enterprise"), s.Features, "plan default flags seeded")

	// A second org with the same name still provisions (slug made unique).
	id2, err := repo.CreateOrg(ctx, "Acme Holdings", "standard", "eu-west-1")
	require.NoError(t, err, "a taken slug must not fail provisioning")
	require.NotEqual(t, id, id2)

	// Feature-flag sync goes through org_set_feature_flags.
	flags := model.FeatureFlags{AIEnabled: true, DataRooms: true}
	require.NoError(t, repo.UpdateFeatureFlags(ctx, id, &flags))
	got, err := repo.GetFeatureFlags(ctx, id)
	require.NoError(t, err)
	require.Equal(t, flags, *got)

	// Grace-period suspension goes through org_set_status.
	require.NoError(t, repo.SuspendOrg(ctx, id))
	var status string
	require.NoError(t, db.Super.QueryRow(ctx,
		`SELECT settings->>'status' FROM organizations WHERE id = $1`, tenant).Scan(&status))
	require.Equal(t, "suspended", status)
	var planAfter string
	require.NoError(t, db.Super.QueryRow(ctx,
		`SELECT plan FROM organizations WHERE id = $1`, tenant).Scan(&planAfter))
	require.Equal(t, "enterprise", planAfter, "suspension must not rewrite the plan")

	// Unknown tenant: the definers report no row, the repository says so.
	require.Error(t, repo.SuspendOrg(ctx, uuid.NewString()))
	require.Error(t, repo.UpdateFeatureFlags(ctx, uuid.NewString(), &flags))

	// dms_app still cannot write organizations directly.
	_, err = db.App.Exec(ctx, `UPDATE organizations SET plan = 'dedicated' WHERE id = $1`, tenant)
	require.Error(t, err, "the direct-write revoke must stay in force")
}
