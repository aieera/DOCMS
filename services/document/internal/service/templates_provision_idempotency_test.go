//go:build integration
// +build integration

// Run with: go test -tags integration ./services/document/internal/service/...
//
// Idempotency for ProvisionFromTemplate (ADR 0118, audit gap note): a retry
// carrying the same client idempotency key must REPLAY the first provision's
// result rather than scaffold a duplicate folder/doc tree.
//
// This file is deliberately self-contained — it stands up its own container +
// harness (newProvisionHarness) rather than borrowing document_service_test.go's
// harness, so it compiles and runs on the base branch regardless of that file's
// shape. Only stubPolicy (a stable same-package symbol) is shared.
package service_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/document/internal/repository"
	"github.com/aieera/sedoc/services/document/internal/service"
)

// oneFolderOneDocTemplate is the smallest template that exercises both
// counters and a variable: one root folder holding one placeholder doc, both
// naming a {{project}} variable so a differing value yields a different digest.
const oneFolderOneDocTemplate = `{
  "nodes": [
    {
      "name": "{{project}}-root",
      "docs": [ { "title": "{{project}} charter" } ]
    }
  ]
}`

type provisionHarness struct {
	ctx    context.Context
	svc    *service.DocumentService
	pool   *pgxpool.Pool
	tenant uuid.UUID
	user   uuid.UUID
}

func newProvisionHarness(t *testing.T) *provisionHarness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)

	dsn, cleanup, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	require.NoError(t, database.RunMigrations(dsn, "../../migrations"))

	// The testcontainer superuser has BYPASSRLS, so opt out of the posture
	// gate (as the sibling integration tests do).
	poolCfg := database.DefaultPoolConfig()
	poolCfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, poolCfg)
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })

	repos := repository.New(pool)
	svc := service.New(pool, repos, stubPolicy{allow: true}, zerolog.Nop())

	tenant := uuid.Must(uuid.NewV7())
	user := uuid.Must(uuid.NewV7())
	// Seed the FK parents provisioning assumes: the org (tenants are
	// organizations) and the acting user (folder/doc created_by + updated_by).
	_, err = pool.Exec(ctx, `
		INSERT INTO organizations (id, name, slug, plan, primary_region)
		VALUES ($1, 'Test Org', $2, 'standard', 'us-east-1')`,
		tenant, "t-"+tenant.String()[:8])
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO users (tenant_id, id, email, display_name, role, status)
		VALUES ($1, $2, $3, 'Test User', 'admin', 'active')`,
		tenant, user, "u-"+user.String()[:8]+"@test.local")
	require.NoError(t, err)

	ctx = auth.SetTenantID(ctx, tenant)
	ctx = auth.WithUser(ctx, auth.UserInfo{ID: user, TenantID: tenant, Role: "admin"})

	return &provisionHarness{ctx: ctx, svc: svc, pool: pool, tenant: tenant, user: user}
}

func (h *provisionHarness) seedWorkspace(t *testing.T) uuid.UUID {
	t.Helper()
	wsID := uuid.Must(uuid.NewV7())
	_, err := h.pool.Exec(h.ctx, `
		INSERT INTO workspaces (tenant_id, id, name, region_pin, created_by)
		VALUES ($1, $2, 'ws', 'us-east-1', $3)
		ON CONFLICT DO NOTHING`, h.tenant, wsID, h.user)
	require.NoError(t, err)
	return wsID
}

func (h *provisionHarness) createTemplate(t *testing.T) uuid.UUID {
	t.Helper()
	tpl, err := h.svc.CreateTemplate(h.ctx, service.TemplateInput{
		Name:       "Project starter",
		Definition: json.RawMessage(oneFolderOneDocTemplate),
	})
	require.NoError(t, err)
	return tpl.ID
}

// folderCount reports how many folders exist in a workspace (the harness pool
// is a BYPASSRLS superuser, so scoping on tenant_id + workspace_id keeps the
// count to this test's tree).
func (h *provisionHarness) folderCount(t *testing.T, wsID uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, h.pool.QueryRow(h.ctx,
		`SELECT count(*) FROM folders WHERE tenant_id = $1 AND workspace_id = $2`,
		h.tenant, wsID).Scan(&n))
	return n
}

// A replay under the same key returns the ORIGINAL result and provisions no
// duplicate tree.
func TestProvisionFromTemplate_IdempotentReplay(t *testing.T) {
	h := newProvisionHarness(t)
	wsID := h.seedWorkspace(t)
	tplID := h.createTemplate(t)

	in := service.ProvisionInput{
		TemplateID:     tplID,
		WorkspaceID:    wsID,
		Variables:      map[string]string{"project": "apollo"},
		IdempotencyKey: "provision-key-1",
	}

	first, err := h.svc.ProvisionFromTemplate(h.ctx, in)
	require.NoError(t, err)
	require.Equal(t, 1, first.FoldersCreated)
	require.Equal(t, 1, first.DocsCreated)
	require.Len(t, first.RootFolderIDs, 1)
	require.Equal(t, 1, h.folderCount(t, wsID))

	// Retry with the identical request + key: same result, no new tree.
	second, err := h.svc.ProvisionFromTemplate(h.ctx, in)
	require.NoError(t, err)
	require.Equal(t, first.RootFolderIDs, second.RootFolderIDs, "replay must return the original root ids")
	require.Equal(t, first.FoldersCreated, second.FoldersCreated)
	require.Equal(t, first.DocsCreated, second.DocsCreated)
	require.Equal(t, 1, h.folderCount(t, wsID), "replay must not scaffold a duplicate tree")
}

// Reusing a key for a materially different request is a conflict, not a
// silent replay of the unrelated result.
func TestProvisionFromTemplate_DigestMismatchConflict(t *testing.T) {
	h := newProvisionHarness(t)
	wsID := h.seedWorkspace(t)
	tplID := h.createTemplate(t)

	_, err := h.svc.ProvisionFromTemplate(h.ctx, service.ProvisionInput{
		TemplateID:     tplID,
		WorkspaceID:    wsID,
		Variables:      map[string]string{"project": "apollo"},
		IdempotencyKey: "provision-key-2",
	})
	require.NoError(t, err)

	_, err = h.svc.ProvisionFromTemplate(h.ctx, service.ProvisionInput{
		TemplateID:     tplID,
		WorkspaceID:    wsID,
		Variables:      map[string]string{"project": "gemini"}, // different digest, same key
		IdempotencyKey: "provision-key-2",
	})
	require.Error(t, err)
	require.Equal(t, vdmserr.KindConflict, vdmserr.KindOf(err), "key reuse with a different request must conflict")
	require.Equal(t, 1, h.folderCount(t, wsID), "the conflicting retry must not scaffold anything")
}

// No key = no idempotency: two calls provision two independent trees
// (unchanged pre-idempotency behavior).
func TestProvisionFromTemplate_NoKeyProvisionsEachTime(t *testing.T) {
	h := newProvisionHarness(t)
	wsID := h.seedWorkspace(t)
	tplID := h.createTemplate(t)

	in := service.ProvisionInput{
		TemplateID:  tplID,
		WorkspaceID: wsID,
		Variables:   map[string]string{"project": "apollo"},
	}
	_, err := h.svc.ProvisionFromTemplate(h.ctx, in)
	require.NoError(t, err)
	_, err = h.svc.ProvisionFromTemplate(h.ctx, in)
	require.NoError(t, err)

	require.Equal(t, 2, h.folderCount(t, wsID), "keyless calls each provision a fresh tree")
}
