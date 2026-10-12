//go:build integration && prodposture

// Document migration 000111 — the organizations write path for the
// production dms_app role (which holds only SELECT on organizations).
// Pins each SECURITY DEFINER function's contract for its callers
// (auth region-pin today; billing provisioning/suspension/flags next).
package repository_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/auth/internal/testenv"
)

func TestProdPosture_OrgMutationDefiners(t *testing.T) {
	testutil.AssertProdPosture(t)
	ctx, db := testenv.ProdPosture(t)

	// Direct DML is still refused for the app role.
	_, err := db.App.Exec(ctx, `INSERT INTO organizations (name, slug) VALUES ('x', 'direct-insert')`)
	require.Error(t, err, "dms_app must not INSERT into organizations directly")

	// org_create
	id := uuid.Must(uuid.NewV7())
	var got uuid.UUID
	require.NoError(t, db.App.QueryRow(ctx,
		`SELECT org_create($1, $2, $3, $4, $5, $6)`,
		id, "Acme", "  ACME-Prod ", "enterprise", "eu-west-1", `{"features":{"ocr":true}}`).Scan(&got))
	require.Equal(t, id, got)
	var slug, plan, region string
	require.NoError(t, db.Super.QueryRow(ctx,
		`SELECT slug, plan, primary_region FROM organizations WHERE id = $1`, id).Scan(&slug, &plan, &region))
	require.Equal(t, "acme-prod", slug)
	require.Equal(t, "enterprise", plan)
	require.Equal(t, "eu-west-1", region)

	var generated uuid.UUID
	require.NoError(t, db.App.QueryRow(ctx,
		`SELECT org_create(NULL, 'Beta', 'beta-'||$1::text, NULL, NULL, NULL)`, uuid.NewString()[:8]).Scan(&generated))
	require.NotEqual(t, uuid.Nil, generated, "NULL id generates one; NULL plan/region/settings default")
	_, err = db.App.Exec(ctx, `SELECT org_create(NULL, 'Dup', 'acme-prod', NULL, NULL, NULL)`)
	require.Error(t, err, "slug stays unique")

	// org_set_region_pin
	var ok bool
	require.NoError(t, db.App.QueryRow(ctx, `SELECT org_set_region_pin($1, 'me-central-1')`, id).Scan(&ok))
	require.True(t, ok)
	require.NoError(t, db.App.QueryRow(ctx, `SELECT org_set_region_pin($1, 'eu-west-1')`, uuid.New()).Scan(&ok))
	require.False(t, ok, "unknown org → false")
	_, err = db.App.Exec(ctx, `SELECT org_set_region_pin($1, '  ')`, id)
	require.Error(t, err, "blank region refused")

	// org_set_status
	require.NoError(t, db.App.QueryRow(ctx, `SELECT org_set_status($1, 'suspended')`, id).Scan(&ok))
	require.True(t, ok)
	var status string
	require.NoError(t, db.Super.QueryRow(ctx, `SELECT settings->>'status' FROM organizations WHERE id = $1`, id).Scan(&status))
	require.Equal(t, "suspended", status)
	_, err = db.App.Exec(ctx, `SELECT org_set_status($1, 'deleted')`, id)
	require.Error(t, err, "unknown status refused")

	// org_set_feature_flags replaces settings.features, keeps other keys.
	require.NoError(t, db.App.QueryRow(ctx, `SELECT org_set_feature_flags($1, '{"ocr":false,"ai":true}')`, id).Scan(&ok))
	require.True(t, ok)
	var features, keptStatus string
	require.NoError(t, db.Super.QueryRow(ctx,
		`SELECT settings->'features'->>'ai', settings->>'status' FROM organizations WHERE id = $1`, id).Scan(&features, &keptStatus))
	require.Equal(t, "true", features)
	require.Equal(t, "suspended", keptStatus)
	_, err = db.App.Exec(ctx, `SELECT org_set_feature_flags($1, '[1,2]')`, id)
	require.Error(t, err, "flags must be an object")

	// EXECUTE is granted to dms_app only — not PUBLIC.
	var publicCan bool
	require.NoError(t, db.Super.QueryRow(ctx,
		`SELECT has_function_privilege('dms_readonly', 'org_create(uuid,text,text,text,text,jsonb)', 'EXECUTE')`).Scan(&publicCan))
	require.False(t, publicCan, "no EXECUTE for roles other than dms_app")
}
