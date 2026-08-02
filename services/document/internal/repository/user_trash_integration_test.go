//go:build integration

// Two-tier trash — the state machine that lets a member "delete
// permanently" without being able to destroy anything.
//
//	deleted_at NULL                       → live
//	deleted_at SET, user_cleared_at NULL  → deleter's Trash AND admin Trash
//	deleted_at SET, user_cleared_at SET   → admin Trash only, still restorable
//
// The invariant worth protecting: clearing is NOT a delete. If
// ClearFromUserTrash ever starts removing the row (or the admin listing
// starts filtering on user_cleared_at), a member gains the ability to
// destroy content an admin may need back — silently, because the member's
// UI would look identical either way.
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
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/model"
)

// seedTrashDoc inserts a minimal live document and returns its id.
func seedTrashDoc(ctx context.Context, t *testing.T, tx pgx.Tx, tenantID, workspaceID, folderID, createdBy uuid.UUID, title string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := tx.Exec(ctx, `
		INSERT INTO documents (tenant_id, id, workspace_id, folder_id, title,
		                       lifecycle_state, created_by, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,'draft',$6, now(), now())`,
		tenantID, id, workspaceID, folderID, title, createdBy)
	if err != nil {
		t.Fatalf("seed document: %v", err)
	}
	return id
}

func TestUserTrash_ClearHidesFromOwnerButNotAdmin(t *testing.T) {
	pool := mustOpenPool(t)
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	repo := &documentRepo{}

	var (
		tenantID    uuid.UUID
		workspaceID uuid.UUID
		folderID    uuid.UUID
		alice       uuid.UUID
		bob         uuid.UUID
	)
	// Find a tenant that has a workspace, a folder and two distinct users.
	row := pool.QueryRow(ctx, `
		SELECT w.tenant_id, w.id, f.id,
		       (SELECT id FROM users u WHERE u.tenant_id = w.tenant_id ORDER BY u.created_at     LIMIT 1),
		       (SELECT id FROM users u WHERE u.tenant_id = w.tenant_id ORDER BY u.created_at DESC LIMIT 1)
		  FROM workspaces w
		  JOIN folders f ON f.tenant_id = w.tenant_id AND f.workspace_id = w.id
		 WHERE (SELECT count(*) FROM users u WHERE u.tenant_id = w.tenant_id) >= 2
		 LIMIT 1`)
	if err := row.Scan(&tenantID, &workspaceID, &folderID, &alice, &bob); err != nil {
		t.Skipf("no seed tenant with 2+ users available: %v", err)
	}

	// Everything runs in one tx that is always rolled back, so the test
	// leaves no residue in the shared dev database.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL app.current_tenant = '`+tenantID.String()+`'`); err != nil {
		t.Fatalf("set tenant guc: %v", err)
	}

	docID := seedTrashDoc(ctx, t, tx, tenantID, workspaceID, folderID, alice, "user-trash-fixture.pdf")

	mineFilter := func(user uuid.UUID) model.DocumentFilter {
		return model.DocumentFilter{
			DeletedOnly: true, DeletedBy: &user, NotUserCleared: true,
			PageSize: 50, SortBy: "updated_at", SortOrder: "desc",
		}
	}
	adminFilter := model.DocumentFilter{
		DeletedOnly: true, PageSize: 200, SortBy: "updated_at", SortOrder: "desc",
	}
	contains := func(f model.DocumentFilter) bool {
		t.Helper()
		page, lErr := repo.List(ctx, tx, tenantID, f)
		if lErr != nil {
			t.Fatalf("list: %v", lErr)
		}
		for i := range page.Items {
			if page.Items[i].ID == docID {
				return true
			}
		}
		return false
	}

	// Live: in nobody's trash.
	if contains(mineFilter(alice)) || contains(adminFilter) {
		t.Fatal("a live document must not appear in any trash listing")
	}

	// Alice deletes it → visible in HER trash and in the admin trash.
	if err := repo.SoftDelete(ctx, tx, tenantID, docID, alice); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	if !contains(mineFilter(alice)) {
		t.Error("after delete: missing from the deleter's own trash")
	}
	if !contains(adminFilter) {
		t.Error("after delete: missing from the admin trash")
	}
	if contains(mineFilter(bob)) {
		t.Error("after delete: another member must not see it in their trash")
	}

	// Bob cannot clear what he did not delete.
	if err := repo.ClearFromUserTrash(ctx, tx, tenantID, docID, bob); err != vdmserr.ErrNotFound {
		t.Errorf("clear by a non-deleter = %v, want ErrNotFound", err)
	}
	if !contains(mineFilter(alice)) {
		t.Error("a failed clear by another member must not affect the owner's trash")
	}

	// Alice clears it — the member-facing "delete permanently".
	if err := repo.ClearFromUserTrash(ctx, tx, tenantID, docID, alice); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if contains(mineFilter(alice)) {
		t.Error("after clear: still in the deleter's trash")
	}
	if !contains(adminFilter) {
		t.Fatal("after clear: GONE from the admin trash — a member just destroyed admin-recoverable content")
	}

	// The row itself must be untouched: clearing is a view change only.
	var stillThere bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM documents WHERE tenant_id=$1 AND id=$2)`,
		tenantID, docID).Scan(&stillThere); err != nil {
		t.Fatalf("existence check: %v", err)
	}
	if !stillThere {
		t.Fatal("after clear: the document row was deleted")
	}

	// Clearing twice is not an error the caller should see as success.
	if err := repo.ClearFromUserTrash(ctx, tx, tenantID, docID, alice); err != vdmserr.ErrNotFound {
		t.Errorf("second clear = %v, want ErrNotFound", err)
	}

	// Admin restore brings it fully back and resets BOTH timestamps — a
	// restore that left user_cleared_at set would return the document to
	// the workspace while staying invisible in its owner's trash forever.
	if err := repo.Restore(ctx, tx, tenantID, docID); err != nil {
		t.Fatalf("restore: %v", err)
	}
	var deletedAt, clearedAt *time.Time
	if err := tx.QueryRow(ctx,
		`SELECT deleted_at, user_cleared_at FROM documents WHERE tenant_id=$1 AND id=$2`,
		tenantID, docID).Scan(&deletedAt, &clearedAt); err != nil {
		t.Fatalf("post-restore read: %v", err)
	}
	if deletedAt != nil {
		t.Error("restore left deleted_at set")
	}
	if clearedAt != nil {
		t.Error("restore left user_cleared_at set — the doc is live but still hidden from its owner's trash")
	}
	if contains(adminFilter) {
		t.Error("after restore: still listed in the admin trash")
	}
}
