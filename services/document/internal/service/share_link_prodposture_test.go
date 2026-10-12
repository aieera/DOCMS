//go:build integration && prodposture
// +build integration,prodposture

// M-26 — public share links under the production RLS role.
//
// The anonymous share-link paths (GET /api/v1/shared/{token} and the bytes
// path /api/v1/shared/{token}/download) resolve the link by its token hash
// BEFORE the tenant is known. share_links is FORCE ROW LEVEL SECURITY keyed
// on app.current_tenant, so a lookup in a plain transaction with no GUC
// reads zero rows (or 22P02 on a pooled connection with ” residue) under
// the NOBYPASSRLS dms_app role: every public share link 404s in production.
//
// The fix routes the pre-tenant read through a SECURITY DEFINER function
// (migration 000105) that returns only the owning tenant, then reads the
// link itself inside database.WithTenantTx. This test drives both public
// paths end to end against the real document migration chain with the
// service constructed on the dms_app pool.
//
// Run:
//
//	go test -tags "integration prodposture" -run '^TestProdPosture_ShareLinkResolves$' \
//	  ./internal/service/
package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/auth"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/document/internal/repository"
	"github.com/aieera/sedoc/services/document/internal/service"
)

func TestProdPosture_ShareLinkResolves(t *testing.T) {
	testutil.AssertProdPosture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	t.Cleanup(cancel)

	db := testutil.NewProdPostureDB(ctx, t, "../../migrations")

	tenant := uuid.Must(uuid.NewV7())
	user := uuid.Must(uuid.NewV7())
	ws := uuid.Must(uuid.NewV7())
	folder := uuid.Must(uuid.NewV7())
	doc := uuid.Must(uuid.NewV7())
	blob := uuid.Must(uuid.NewV7())
	version := uuid.Must(uuid.NewV7())

	// Out-of-band seeding as the superuser (ops-tooling split).
	seedOrg(ctx, t, db.Super, tenant)
	seedUser(ctx, t, db.Super, tenant, user)
	seedWorkspaceFolderDoc(ctx, t, db.Super, tenant, ws, folder, doc)
	seedContentBlob(ctx, t, db.Super, tenant, blob, "share-bucket", "k/"+blob.String(), 42, "application/pdf")
	_, err := db.Super.Exec(ctx, `
		INSERT INTO document_versions
			(tenant_id, id, document_id, version_number, content_blob_id, size_bytes, mime_type, sha256_hash)
		VALUES ($1, $2, $3, 1, $4, 42, 'application/pdf', $5)`,
		tenant, version, doc, blob, "sha-"+blob.String()[:8])
	require.NoError(t, err)
	_, err = db.Super.Exec(ctx,
		`UPDATE documents SET current_version_id = $3 WHERE tenant_id = $1 AND id = $2`,
		tenant, doc, version)
	require.NoError(t, err)

	// Mint two links through the real service on the dms_app pool (the
	// authenticated create path runs in WithTenantTx and must already work).
	appSvc := service.New(db.App, repository.New(db.App), stubPolicy{allow: true}, zerolog.Nop())
	callerCtx := auth.WithUser(auth.SetTenantID(ctx, tenant), auth.UserInfo{ID: user, TenantID: tenant, Role: "admin"})
	viewLink, err := appSvc.CreateShareLink(callerCtx, &service.CreateShareLinkInput{
		DocumentID: doc, Permissions: []string{"view"},
	})
	require.NoError(t, err)
	dlLink, err := appSvc.CreateShareLink(callerCtx, &service.CreateShareLinkInput{
		DocumentID: doc, Permissions: []string{"view", "download"},
	})
	require.NoError(t, err)

	// A fresh pool connection state is irrelevant: the anonymous paths must
	// work with NO tenant on ctx, exactly like the public HTTP routes.
	anon := context.Background()

	res, err := appSvc.PeekShareLink(anon, viewLink.Token)
	require.NoError(t, err, "anonymous share-link browse must resolve under dms_app NOBYPASSRLS")
	require.NotNil(t, res.Document)
	require.Equal(t, doc, res.Document.ID)
	require.Empty(t, res.DownloadURL, "view-only link must not surface a download URL")

	// Same lookup again — a pooled connection now carries GUC residue ('')
	// from the WithTenantTx above; the definer path must not care.
	res, err = appSvc.PeekShareLink(anon, viewLink.Token)
	require.NoError(t, err)
	require.Equal(t, doc, res.Document.ID)

	dl, err := appSvc.ResolveShareDownload(anon, dlLink.Token, "")
	require.NoError(t, err, "anonymous share-link download must resolve under dms_app NOBYPASSRLS")
	require.False(t, dl.PasswordRequired)
	require.Equal(t, tenant, dl.TenantID)
	require.Equal(t, version, dl.VersionID)
	require.Equal(t, "share-bucket", dl.Bucket)
	require.Equal(t, "k/"+blob.String(), dl.Key)

	var views int
	require.NoError(t, db.Super.QueryRow(ctx,
		`SELECT view_count FROM share_links WHERE tenant_id = $1 AND id = $2`,
		tenant, dlLink.ID).Scan(&views))
	require.Equal(t, 1, views, "download path bumps the view counter inside the tenant tx")

	// Unknown token: still a clean not-found, never an error leak.
	_, err = appSvc.PeekShareLink(anon, "definitely-not-a-real-token")
	require.ErrorIs(t, err, vdmserr.ErrNotFound)

}
