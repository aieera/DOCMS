// Pure-Go tests on the compiled Rego policy. No database, no Redis —
// we feed input + data directly through Engine.Eval.
package opa

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/services/policy/internal/model"
)

// compiles once; reused across tests.
func mustEngine(t *testing.T) *Engine {
	t.Helper()
	e, err := New(context.Background())
	require.NoError(t, err)
	return e
}

// eval is a helper that constructs a minimal EvalInput and runs it.
func eval(t *testing.T, e *Engine, in model.CheckInput, perms []PermissionDoc, groups []string, ws []WorkspaceDoc) model.CheckResult {
	t.Helper()
	r, _, err := e.Eval(context.Background(), EvalInput{
		Input: in, Permissions: perms, UserGroups: groups, Workspaces: ws,
	})
	require.NoError(t, err)
	return r
}

// ---- Rule 1: direct grant --------------------------------------------------

func TestDirectGrantAllowsView(t *testing.T) {
	e := mustEngine(t)
	in := model.CheckInput{
		SubjectType: "user", SubjectID: "u1",
		Action: "view", ResourceType: "document", ResourceID: "d1",
		Context: map[string]string{},
	}
	perms := []PermissionDoc{{
		ResourceType: "document", ResourceID: "d1",
		PrincipalType: "user", PrincipalID: "u1",
		Capability: "view",
	}}
	require.True(t, eval(t, e, in, perms, nil, nil).Allowed)
}

func TestNoGrantDenies(t *testing.T) {
	e := mustEngine(t)
	in := model.CheckInput{
		SubjectType: "user", SubjectID: "u1",
		Action: "view", ResourceType: "document", ResourceID: "d1",
		Context: map[string]string{},
	}
	require.False(t, eval(t, e, in, nil, nil, nil).Allowed)
}

// ---- Rule 2: group membership ---------------------------------------------

func TestGroupGrantAllows(t *testing.T) {
	e := mustEngine(t)
	in := model.CheckInput{
		SubjectType: "user", SubjectID: "u1",
		Action: "view", ResourceType: "document", ResourceID: "d1",
		Context: map[string]string{},
	}
	perms := []PermissionDoc{{
		ResourceType: "document", ResourceID: "d1",
		PrincipalType: "group", PrincipalID: "g1",
		Capability: "view",
	}}
	require.True(t, eval(t, e, in, perms, []string{"g1"}, nil).Allowed)
	// Different group → no access.
	require.False(t, eval(t, e, in, perms, []string{"g99"}, nil).Allowed)
}

// ---- Capability hierarchy --------------------------------------------------

func TestCapabilityHierarchy(t *testing.T) {
	e := mustEngine(t)
	base := func(action string) model.CheckInput {
		return model.CheckInput{
			SubjectType: "user", SubjectID: "u1", Action: action,
			ResourceType: "document", ResourceID: "d1",
			Context: map[string]string{},
		}
	}
	admin := []PermissionDoc{{
		ResourceType: "document", ResourceID: "d1",
		PrincipalType: "user", PrincipalID: "u1", Capability: "admin",
	}}
	view := []PermissionDoc{{
		ResourceType: "document", ResourceID: "d1",
		PrincipalType: "user", PrincipalID: "u1", Capability: "view",
	}}
	// admin can view, edit, delete
	require.True(t, eval(t, e, base("view"), admin, nil, nil).Allowed)
	require.True(t, eval(t, e, base("edit"), admin, nil, nil).Allowed)
	require.True(t, eval(t, e, base("delete"), admin, nil, nil).Allowed)
	// view cannot edit
	require.True(t, eval(t, e, base("view"), view, nil, nil).Allowed)
	require.False(t, eval(t, e, base("edit"), view, nil, nil).Allowed)
}

// ---- view_unredacted (ADR 0079) -------------------------------------------

// view_unredacted sits in the hierarchy between view (10) and share (20)
// at rank 15. The policy invariant is asymmetric on purpose:
//   - granting `view` does NOT cascade to view_unredacted (that defeats
//     the whole point of redacting on the visible version)
//   - granting any of {share, edit, delete, admin} DOES cascade
//   - owner / admin via Rule 6 still pass automatically

func TestViewUnredacted_DirectGrantAllows(t *testing.T) {
	e := mustEngine(t)
	in := model.CheckInput{
		SubjectType: "user", SubjectID: "u1",
		Action: "view_unredacted", ResourceType: "document", ResourceID: "d1",
		Context: map[string]string{},
	}
	perms := []PermissionDoc{{
		ResourceType: "document", ResourceID: "d1",
		PrincipalType: "user", PrincipalID: "u1",
		Capability: "view_unredacted",
	}}
	require.True(t, eval(t, e, in, perms, nil, nil).Allowed)
}

func TestViewUnredacted_OwnerRolePasses(t *testing.T) {
	e := mustEngine(t)
	in := model.CheckInput{
		SubjectType: "user", SubjectID: "u1",
		Action: "view_unredacted", ResourceType: "document", ResourceID: "d1",
		Context: map[string]string{"user_role": "owner"},
	}
	require.True(t, eval(t, e, in, nil, nil, nil).Allowed)
}

func TestViewUnredacted_AdminRolePasses(t *testing.T) {
	e := mustEngine(t)
	in := model.CheckInput{
		SubjectType: "user", SubjectID: "u1",
		Action: "view_unredacted", ResourceType: "document", ResourceID: "d1",
		Context: map[string]string{"user_role": "admin"},
	}
	require.True(t, eval(t, e, in, nil, nil, nil).Allowed)
}

func TestViewUnredacted_ViewGrantDoesNotCascade(t *testing.T) {
	// THIS is the load-bearing assertion of ADR 0079 — a user with
	// `view` on the document must NOT be able to see the source of a
	// redacted version. If this ever flips to true the privacy
	// guarantee of redact-then-share is broken.
	e := mustEngine(t)
	in := model.CheckInput{
		SubjectType: "user", SubjectID: "u1",
		Action: "view_unredacted", ResourceType: "document", ResourceID: "d1",
		Context: map[string]string{},
	}
	perms := []PermissionDoc{{
		ResourceType: "document", ResourceID: "d1",
		PrincipalType: "user", PrincipalID: "u1",
		Capability: "view",
	}}
	require.False(t, eval(t, e, in, perms, nil, nil).Allowed,
		"granting `view` must NOT cascade to view_unredacted")
}

func TestViewUnredacted_ShareEditDeleteAdminAllCascade(t *testing.T) {
	// share/edit/delete/admin sit above view_unredacted in the
	// hierarchy and intentionally cascade — operators with those
	// caps already see the source through other paths anyway.
	e := mustEngine(t)
	for _, cap := range []string{"share", "edit", "delete", "admin"} {
		in := model.CheckInput{
			SubjectType: "user", SubjectID: "u1",
			Action: "view_unredacted", ResourceType: "document", ResourceID: "d1",
			Context: map[string]string{},
		}
		perms := []PermissionDoc{{
			ResourceType: "document", ResourceID: "d1",
			PrincipalType: "user", PrincipalID: "u1",
			Capability: cap,
		}}
		require.True(t, eval(t, e, in, perms, nil, nil).Allowed,
			"capability %s must cascade to view_unredacted", cap)
	}
}

func TestViewUnredacted_NoGrantNoRoleDenies(t *testing.T) {
	// Member role with no explicit grant — the realistic "tenant
	// member opens a redacted doc and clicks /unredacted" path.
	// 403 is what we want.
	e := mustEngine(t)
	in := model.CheckInput{
		SubjectType: "user", SubjectID: "u1",
		Action: "view_unredacted", ResourceType: "document", ResourceID: "d1",
		Context: map[string]string{"user_role": "member"},
	}
	require.False(t, eval(t, e, in, nil, nil, nil).Allowed)
}

func TestViewUnredacted_GuestRoleDenies(t *testing.T) {
	e := mustEngine(t)
	in := model.CheckInput{
		SubjectType: "user", SubjectID: "u1",
		Action: "view_unredacted", ResourceType: "document", ResourceID: "d1",
		Context: map[string]string{"user_role": "guest"},
	}
	require.False(t, eval(t, e, in, nil, nil, nil).Allowed)
}

func TestViewUnredacted_GroupGrantAllows(t *testing.T) {
	// Compliance reviewers typically get the cap via group membership,
	// not direct user grant. Make sure group cascade works for the
	// new capability.
	e := mustEngine(t)
	in := model.CheckInput{
		SubjectType: "user", SubjectID: "u1",
		Action: "view_unredacted", ResourceType: "document", ResourceID: "d1",
		Context: map[string]string{},
	}
	perms := []PermissionDoc{{
		ResourceType: "document", ResourceID: "d1",
		PrincipalType: "group", PrincipalID: "compliance-reviewers",
		Capability: "view_unredacted",
	}}
	require.True(t, eval(t, e, in, perms, []string{"compliance-reviewers"}, nil).Allowed)
}

// ---- Rule 3: folder cascade -----------------------------------------------

func TestFolderCascadeToDocument(t *testing.T) {
	e := mustEngine(t)
	in := model.CheckInput{
		SubjectType: "user", SubjectID: "u1", Action: "view",
		ResourceType: "document", ResourceID: "d1",
		Context: map[string]string{"folder_id": "f1"},
	}
	perms := []PermissionDoc{{
		ResourceType: "folder", ResourceID: "f1",
		PrincipalType: "user", PrincipalID: "u1", Capability: "edit",
	}}
	require.True(t, eval(t, e, in, perms, nil, nil).Allowed)
}

// ---- Rule 5: workspace admin ----------------------------------------------

func TestWorkspaceAdminAllPermissions(t *testing.T) {
	e := mustEngine(t)
	in := model.CheckInput{
		SubjectType: "user", SubjectID: "u1", Action: "delete",
		ResourceType: "document", ResourceID: "d1",
		Context: map[string]string{"workspace_id": "w1"},
	}
	ws := []WorkspaceDoc{{UserID: "u1", WorkspaceID: "w1", Role: "admin"}}
	require.True(t, eval(t, e, in, nil, nil, ws).Allowed)
	// Non-admin workspace member cannot delete.
	ws[0].Role = "member"
	require.False(t, eval(t, e, in, nil, nil, ws).Allowed)
}

// ---- Rule 6: org owner/admin ---------------------------------------------

func TestOrgAdminAllPermissions(t *testing.T) {
	e := mustEngine(t)
	in := model.CheckInput{
		SubjectType: "user", SubjectID: "u1", Action: "admin",
		ResourceType: "document", ResourceID: "d1",
		Context: map[string]string{"user_role": "owner"},
	}
	require.True(t, eval(t, e, in, nil, nil, nil).Allowed)
	in.Context["user_role"] = "admin"
	require.True(t, eval(t, e, in, nil, nil, nil).Allowed)
	in.Context["user_role"] = "member"
	require.False(t, eval(t, e, in, nil, nil, nil).Allowed)
}

// ---- Deny rules -----------------------------------------------------------

func TestDisposedDocumentBlockedForNonAdmin(t *testing.T) {
	e := mustEngine(t)
	in := model.CheckInput{
		SubjectType: "user", SubjectID: "u1", Action: "view",
		ResourceType: "document", ResourceID: "d1",
		Context: map[string]string{
			"lifecycle_state": "disposed",
			"user_role":       "member",
		},
	}
	// Even with a direct grant, disposed wins.
	perms := []PermissionDoc{{
		ResourceType: "document", ResourceID: "d1",
		PrincipalType: "user", PrincipalID: "u1", Capability: "admin",
	}}
	require.False(t, eval(t, e, in, perms, nil, nil).Allowed)

	// Org admin bypasses the disposed block.
	in.Context["user_role"] = "admin"
	require.True(t, eval(t, e, in, perms, nil, nil).Allowed)
}

func TestDeactivatedUserBlocked(t *testing.T) {
	e := mustEngine(t)
	in := model.CheckInput{
		SubjectType: "user", SubjectID: "u1", Action: "view",
		ResourceType: "document", ResourceID: "d1",
		Context: map[string]string{"user_status": "deactivated", "user_role": "owner"},
	}
	perms := []PermissionDoc{{
		ResourceType: "document", ResourceID: "d1",
		PrincipalType: "user", PrincipalID: "u1", Capability: "admin",
	}}
	require.False(t, eval(t, e, in, perms, nil, nil).Allowed)
}

// ---- Expired permissions --------------------------------------------------

func TestExpiredPermissionDenied(t *testing.T) {
	e := mustEngine(t)
	past := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	in := model.CheckInput{
		SubjectType: "user", SubjectID: "u1", Action: "view",
		ResourceType: "document", ResourceID: "d1",
		Context: map[string]string{},
	}
	perms := []PermissionDoc{{
		ResourceType: "document", ResourceID: "d1",
		PrincipalType: "user", PrincipalID: "u1",
		Capability: "view", ExpiresAt: past,
	}}
	require.False(t, eval(t, e, in, perms, nil, nil).Allowed)
}

// ---- Performance smoke test (not a strict benchmark) ---------------------

func BenchmarkEval(b *testing.B) {
	e, err := New(context.Background())
	require.NoError(b, err)
	in := model.CheckInput{
		SubjectType: "user", SubjectID: "u1", Action: "view",
		ResourceType: "document", ResourceID: "d1",
		Context: map[string]string{"workspace_id": "w1"},
	}
	perms := make([]PermissionDoc, 20)
	for i := range perms {
		perms[i] = PermissionDoc{
			ResourceType: "document", ResourceID: "d1",
			PrincipalType: "user", PrincipalID: "u1", Capability: "view",
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, err := e.Eval(context.Background(), EvalInput{
			Input: in, Permissions: perms,
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}
