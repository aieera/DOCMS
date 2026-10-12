//go:build integration
// +build integration

// D-8 (per-document half) — the sync delta must not hand a member the
// title of a document the policy denies them, nor one the classification /
// clearance gate hides, even inside a shared folder. Tombstones are still
// emitted (so a client drops a copy it may hold) but carry the id only.
package service_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/aieera/sedoc/pkg/auth"
	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/service"
)

// syncACLPolicy allows workspace view, denies documents in denied, and
// applies the clearance deny the real rego applies when the gate is on.
type syncACLPolicy struct{ denied map[string]bool }

func clearanceRank(c string) int {
	return map[string]int{"internal": 1, "confidential": 2, "restricted": 3}[c]
}

func (p syncACLPolicy) decide(in *sedocv1.CheckPermissionRequest) bool {
	if in.GetResourceType() != "document" {
		return true
	}
	if p.denied[in.GetResourceId()] {
		return false
	}
	f := in.GetContext().GetFields()
	if f["classification_gate"].GetStringValue() == "on" &&
		clearanceRank(f["user_clearance"].GetStringValue()) < clearanceRank(f["required_clearance"].GetStringValue()) {
		return false
	}
	return true
}

func (p syncACLPolicy) CheckPermission(_ context.Context, in *sedocv1.CheckPermissionRequest, _ ...grpc.CallOption) (*sedocv1.CheckPermissionResponse, error) {
	return &sedocv1.CheckPermissionResponse{Allowed: p.decide(in)}, nil
}
func (p syncACLPolicy) BatchCheckPermission(_ context.Context, in *sedocv1.BatchCheckPermissionRequest, _ ...grpc.CallOption) (*sedocv1.BatchCheckPermissionResponse, error) {
	out := &sedocv1.BatchCheckPermissionResponse{}
	for _, c := range in.GetChecks() {
		out.Results = append(out.Results, &sedocv1.CheckPermissionResponse{Allowed: p.decide(c)})
	}
	return out, nil
}

func TestSyncDelta_HidesPolicyDeniedDocs(t *testing.T) {
	h := newHarness(t, true)

	member := uuid.Must(uuid.NewV7())
	_, err := h.pool.Exec(h.ctx, `
		INSERT INTO users (tenant_id, id, email, display_name, role, status)
		VALUES ($1, $2, $3, 'U', 'member', 'active')`, h.tenant, member, member.String()+"@test.local")
	require.NoError(t, err)
	_, err = h.pool.Exec(h.ctx, `INSERT INTO classification_gate_config (tenant_id, enabled) VALUES ($1, true)`, h.tenant)
	require.NoError(t, err)

	ws, root := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	_, err = h.pool.Exec(h.ctx, `INSERT INTO workspaces (tenant_id, id, name, region_pin, created_by)
		VALUES ($1, $2, 'ws', 'us-east-1', $3)`, h.tenant, ws, h.user)
	require.NoError(t, err)
	_, err = h.pool.Exec(h.ctx, `INSERT INTO folders (tenant_id, id, workspace_id, name, path, depth)
		VALUES ($1, $2, $3, 'root', 'root', 0)`, h.tenant, root, ws)
	require.NoError(t, err)
	doc := func(title, class string, deleted bool) uuid.UUID {
		id := uuid.Must(uuid.NewV7())
		_, err := h.pool.Exec(h.ctx, `
			INSERT INTO documents (tenant_id, id, workspace_id, folder_id, title, description,
			                       region_pin, sha256_hash, mime_type, security_classification, deleted_at)
			VALUES ($1, $2, $3, $4, $5, '', 'us-east-1', '', 'application/pdf', $6,
			        CASE WHEN $7 THEN now() END)`,
			h.tenant, id, ws, root, title, class, deleted)
		require.NoError(t, err)
		return id
	}
	visible := doc("handbook", "", false)
	denied := doc("board minutes", "", false)
	classified := doc("merger plan", "confidential", false)
	deniedGone := doc("old salaries", "", true)

	policy := syncACLPolicy{denied: map[string]bool{denied.String(): true, deniedGone.String(): true}}
	svc := service.New(h.pool, h.repos, policy, zerolog.Nop())
	memberCtx := auth.WithUser(auth.SetTenantID(h.ctx, h.tenant),
		auth.UserInfo{ID: member, TenantID: h.tenant, Role: "member"})

	res, err := svc.SyncDelta(memberCtx, &ws, nil, "", 500)
	require.NoError(t, err)
	got := map[uuid.UUID]model.SyncChange{}
	for _, c := range res.Changes {
		got[c.ID] = c
	}
	require.Contains(t, got, visible)
	require.Contains(t, got, root)
	require.NotContains(t, got, denied, "a policy-denied document must not appear")
	require.NotContains(t, got, classified, "a document above the caller's clearance must not appear")
	require.Contains(t, got, deniedGone, "tombstones are still emitted")
	tomb := got[deniedGone]
	require.Equal(t, model.SyncOpDelete, tomb.Op)
	require.Empty(t, tomb.Name, "a tombstone for a document the caller cannot view carries the id only")
	require.Nil(t, tomb.ParentID)

	// Page size 1: pages that filter out entirely do not stall the walk,
	// and the walk yields exactly the visible set.
	var walked []uuid.UUID
	cursor := ""
	for i := 0; i < 20; i++ {
		page, err := svc.SyncDelta(memberCtx, &ws, nil, cursor, 1)
		require.NoError(t, err)
		for _, c := range page.Changes {
			walked = append(walked, c.ID)
		}
		cursor = page.NextCursor
		if !page.HasMore {
			break
		}
	}
	require.ElementsMatch(t, []uuid.UUID{root, visible, deniedGone}, walked)
}
