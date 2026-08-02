//go:build integration

// A workspace-scoped grant must make the workspace VISIBLE, not merely
// accessible.
//
// Two authorization sources decide what a member sees:
//   - OPA, over the `permissions` table — used by the document/folder reads
//   - bespoke SQL in workspace_repo.List/HasAccess — used by the sidebar
//
// They disagreed. "Manage access → grant a user view on this workspace"
// writes a permissions row that OPA honours, so the grantee could open
// /workspaces/{id}/documents and get 200 — but List admitted callers only
// via is_default / created_by / workspace_members / folder_grants, so the
// workspace never appeared in their list. Access granted, nothing to click,
// and no error anywhere to explain it.
//
// Run with:
//
//	go test -tags integration ./services/document/internal/repository/...

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestWorkspaceGrant_MakesWorkspaceVisibleAndListConsistentWithHasAccess(t *testing.T) {
	pool := mustOpenPool(t)
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	repo := &workspaceRepo{}

	var tenantID, creator, grantee uuid.UUID
	row := pool.QueryRow(ctx, `
		SELECT o.id,
		       (SELECT id FROM users u WHERE u.tenant_id = o.id ORDER BY u.created_at      LIMIT 1),
		       (SELECT id FROM users u WHERE u.tenant_id = o.id ORDER BY u.created_at DESC LIMIT 1)
		  FROM organizations o
		 WHERE (SELECT count(*) FROM users u WHERE u.tenant_id = o.id) >= 2
		 LIMIT 1`)
	if err := row.Scan(&tenantID, &creator, &grantee); err != nil {
		t.Skipf("no tenant with 2+ users: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL app.current_tenant = '`+tenantID.String()+`'`); err != nil {
		t.Fatalf("set tenant guc: %v", err)
	}

	// A NON-default workspace created by someone else — the grantee must
	// have no other route to it.
	wsID := uuid.New()
	if _, err := tx.Exec(ctx, `
		INSERT INTO workspaces (tenant_id, id, name, created_by, created_at, updated_at, is_default)
		VALUES ($1,$2,'grant-visibility-fixture',$3, now(), now(), false)`,
		tenantID, wsID, creator); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	visible := func(user uuid.UUID) bool {
		t.Helper()
		ws, lErr := repo.List(ctx, tx, tenantID, user, nil, "member")
		if lErr != nil {
			t.Fatalf("list: %v", lErr)
		}
		for i := range ws {
			if ws[i].ID == wsID {
				return true
			}
		}
		return false
	}
	reachable := func(user uuid.UUID) bool {
		t.Helper()
		ok, hErr := repo.HasAccess(ctx, tx, tenantID, wsID, user, nil)
		if hErr != nil {
			t.Fatalf("has access: %v", hErr)
		}
		return ok
	}

	if visible(grantee) || reachable(grantee) {
		t.Fatal("an ungranted member can already see a workspace they have no relationship to")
	}
	if !visible(creator) {
		t.Error("the creator cannot see their own workspace")
	}

	// The grant "Manage access" writes.
	permID := uuid.New()
	if _, err := tx.Exec(ctx, `
		INSERT INTO permissions (tenant_id, id, resource_type, resource_id,
		                         principal_type, principal_id, capability,
		                         granted_by, granted_at, valid_from)
		VALUES ($1,$2,'workspace',$3,'user',$4,'view',$5, now(), now())`,
		tenantID, permID, wsID, grantee, creator); err != nil {
		t.Fatalf("insert grant: %v", err)
	}

	if !visible(grantee) {
		t.Error("after a workspace grant the workspace is still missing from the grantee's list")
	}
	if !reachable(grantee) {
		t.Error("after a workspace grant HasAccess still refuses the grantee")
	}

	// An expired grant must not resurrect visibility, and List/HasAccess
	// must agree about that — a split here is how "shows in the sidebar but
	// 403s on open" happens.
	if _, err := tx.Exec(ctx,
		`UPDATE permissions SET expires_at = now() - interval '1 minute' WHERE tenant_id=$1 AND id=$2`,
		tenantID, permID); err != nil {
		t.Fatalf("expire grant: %v", err)
	}
	if visible(grantee) {
		t.Error("an EXPIRED workspace grant still makes the workspace visible")
	}
	if reachable(grantee) {
		t.Error("an EXPIRED workspace grant still passes HasAccess")
	}

	// Revoked outright.
	if _, err := tx.Exec(ctx, `DELETE FROM permissions WHERE tenant_id=$1 AND id=$2`, tenantID, permID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if visible(grantee) || reachable(grantee) {
		t.Error("a revoked grant still admits the grantee")
	}

	// A tenant owner sees everything regardless of grants.
	ws, err := repo.List(ctx, tx, tenantID, grantee, nil, "owner")
	if err != nil {
		t.Fatalf("owner list: %v", err)
	}
	found := false
	for i := range ws {
		if ws[i].ID == wsID {
			found = true
		}
	}
	if !found {
		t.Error("a tenant owner cannot see an ungranted workspace")
	}
}
