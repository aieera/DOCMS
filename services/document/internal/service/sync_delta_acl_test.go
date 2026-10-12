//go:build integration
// +build integration

// D-8 / D-1 (sync half) — the sync delta feed.
//
// The delta was gated on workspace "view" only, then returned every folder
// and document row in the workspace: a member saw the names and paths of
// private folders they cannot open (and the titles of the documents in
// them), plus the sha256 of every document's content. The sha256 is a blob
// identity in this system (dedup, ERP blob_checksum relinking), so handing
// it to every member widened the blob-reuse surface (D-1).
//
// The delta is now filtered by the caller's reader set — tenant owner/admin
// see everything; everyone else sees shared folders plus private folders
// they own or are granted (directly or via a group), and only documents in
// those folders — and it never carries a content hash.
package service_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/services/document/internal/model"
)

func TestSyncDelta_HidesPrivateFolderDocs(t *testing.T) {
	h := newHarness(t, true) // h.user is a tenant admin; policy allows workspace view

	member, otherOwner := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	for _, u := range []uuid.UUID{member, otherOwner} {
		_, err := h.pool.Exec(h.ctx, `
			INSERT INTO users (tenant_id, id, email, display_name, role, status)
			VALUES ($1, $2, $3, 'U', 'member', 'active')`, h.tenant, u, u.String()+"@test.local")
		require.NoError(t, err)
	}
	group := uuid.Must(uuid.NewV7())
	_, err := h.pool.Exec(h.ctx, `INSERT INTO groups (tenant_id, id, name) VALUES ($1, $2, 'G')`, h.tenant, group)
	require.NoError(t, err)
	_, err = h.pool.Exec(h.ctx, `INSERT INTO group_members (tenant_id, group_id, user_id) VALUES ($1, $2, $3)`,
		h.tenant, group, member)
	require.NoError(t, err)

	ws := uuid.Must(uuid.NewV7())
	_, err = h.pool.Exec(h.ctx, `INSERT INTO workspaces (tenant_id, id, name, region_pin, created_by)
		VALUES ($1, $2, 'ws', 'us-east-1', $3)`, h.tenant, ws, h.user)
	require.NoError(t, err)

	folder := func(name, visibility string, owner *uuid.UUID) uuid.UUID {
		id := uuid.Must(uuid.NewV7())
		_, err := h.pool.Exec(h.ctx, `
			INSERT INTO folders (tenant_id, id, workspace_id, name, path, depth, visibility, owner_id)
			VALUES ($1, $2, $3, $4, $5::ltree, 0, $6, $7)`, h.tenant, id, ws, name, name, visibility, owner)
		require.NoError(t, err)
		return id
	}
	doc := func(folderID uuid.UUID, title string) uuid.UUID {
		id := uuid.Must(uuid.NewV7())
		_, err := h.pool.Exec(h.ctx, `
			INSERT INTO documents (tenant_id, id, workspace_id, folder_id, title, description,
			                       region_pin, sha256_hash, mime_type, total_size_bytes)
			VALUES ($1, $2, $3, $4, $5, '', 'us-east-1', $6, 'application/pdf', 10)`,
			h.tenant, id, ws, folderID, title, "deadbeef"+id.String())
		require.NoError(t, err)
		return id
	}

	shared := folder("shared", "shared", nil)
	private := folder("hr_private", "private", &otherOwner)
	granted := folder("granted_private", "private", &otherOwner)
	_, err = h.pool.Exec(h.ctx, `
		INSERT INTO folder_grants (tenant_id, folder_id, grantee_type, grantee_id)
		VALUES ($1, $2, 'group', $3)`, h.tenant, granted, group)
	require.NoError(t, err)

	sharedDoc := doc(shared, "handbook")
	privateDoc := doc(private, "salaries")
	grantedDoc := doc(granted, "group plan")
	// A deleted document in the private folder: its tombstone must not leak.
	privateGone := doc(private, "layoff list")
	_, err = h.pool.Exec(h.ctx, `UPDATE documents SET deleted_at = now() WHERE tenant_id = $1 AND id = $2`, h.tenant, privateGone)
	require.NoError(t, err)

	ids := func(changes []model.SyncChange) map[uuid.UUID]model.SyncChange {
		out := map[uuid.UUID]model.SyncChange{}
		for _, c := range changes {
			out[c.ID] = c
		}
		return out
	}

	memberCtx := auth.WithUser(auth.SetTenantID(h.ctx, h.tenant),
		auth.UserInfo{ID: member, TenantID: h.tenant, Role: "member", Groups: []uuid.UUID{group}})
	res, err := h.svc.SyncDelta(memberCtx, &ws, nil, "", 500)
	require.NoError(t, err)
	got := ids(res.Changes)

	for _, id := range []uuid.UUID{shared, sharedDoc, granted, grantedDoc} {
		require.Contains(t, got, id, "member must see shared and granted entries")
	}
	for _, id := range []uuid.UUID{private, privateDoc, privateGone} {
		require.NotContains(t, got, id, "member must not see a private folder (or its documents) they cannot open")
	}
	for _, c := range res.Changes {
		require.Empty(t, c.SHA256, "the delta must not carry content hashes (%s %s)", c.Kind, c.Name)
	}

	// The private folder's owner sees it and its tombstone.
	ownerCtx := auth.WithUser(auth.SetTenantID(h.ctx, h.tenant),
		auth.UserInfo{ID: otherOwner, TenantID: h.tenant, Role: "member"})
	res, err = h.svc.SyncDelta(ownerCtx, &ws, nil, "", 500)
	require.NoError(t, err)
	got = ids(res.Changes)
	require.Contains(t, got, private)
	require.Contains(t, got, privateDoc)
	require.Equal(t, model.SyncOpDelete, got[privateGone].Op)

	// Tenant admins see everything.
	res, err = h.svc.SyncDelta(h.ctx, &ws, nil, "", 500)
	require.NoError(t, err)
	got = ids(res.Changes)
	for _, id := range []uuid.UUID{shared, private, granted, sharedDoc, privateDoc, grantedDoc, privateGone} {
		require.Contains(t, got, id)
	}

	// Pagination stays consistent under the filter: page size 1 walks the
	// member's visible set exactly.
	var walked []uuid.UUID
	cursor := ""
	for i := 0; i < 20; i++ {
		page, err := h.svc.SyncDelta(memberCtx, &ws, nil, cursor, 1)
		require.NoError(t, err)
		for _, c := range page.Changes {
			walked = append(walked, c.ID)
		}
		cursor = page.NextCursor
		if !page.HasMore {
			break
		}
	}
	require.ElementsMatch(t, []uuid.UUID{shared, sharedDoc, granted, grantedDoc}, walked)
}
