package service

// I-2 (review of DEF-03) — product decision: whoever can READ a document
// sees every task on it. canReadDocument is the policy round-trip behind
// that rule; these pin its request and its fail-closed contract without a
// database.

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/aieera/sedoc/pkg/auth"
	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
	"github.com/aieera/sedoc/services/task/internal/repository"
)

type recordingPolicy struct {
	allowed bool
	err     error
	req     *sedocv1.CheckPermissionRequest
	md      metadata.MD
	calls   int
}

func (p *recordingPolicy) CheckPermission(ctx context.Context, in *sedocv1.CheckPermissionRequest, _ ...grpc.CallOption) (*sedocv1.CheckPermissionResponse, error) {
	p.calls++
	p.req = in
	p.md, _ = metadata.FromOutgoingContext(ctx)
	if _, ok := ctx.Deadline(); !ok {
		return nil, errors.New("policy call without a deadline")
	}
	if p.err != nil {
		return nil, p.err
	}
	return &sedocv1.CheckPermissionResponse{Allowed: p.allowed}, nil
}

func testFacts() repository.DocumentFacts {
	return repository.DocumentFacts{
		ID:             uuid.Must(uuid.NewV7()),
		WorkspaceID:    uuid.Must(uuid.NewV7()),
		FolderID:       uuid.Must(uuid.NewV7()),
		LifecycleState: "active",
	}
}

func TestCanReadDocument_AsksPolicyForViewWithDocumentContext(t *testing.T) {
	tenant, user := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	ctx := auth.WithUser(context.Background(), auth.UserInfo{ID: user, TenantID: tenant, Role: "member", Email: "carol@example.com"})
	doc := testFacts()
	p := &recordingPolicy{allowed: true}

	ok, err := canReadDocument(ctx, p, tenant, user, "member", doc)

	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 1, p.calls)
	require.Equal(t, "user", p.req.GetSubjectType())
	require.Equal(t, user.String(), p.req.GetSubjectId())
	require.Equal(t, "view", p.req.GetAction())
	require.Equal(t, "document", p.req.GetResourceType())
	require.Equal(t, doc.ID.String(), p.req.GetResourceId())
	// The context OPA's document rules need (role for org-wide roles,
	// workspace/folder for inheritance, lifecycle for the disposed deny) —
	// the same keys the document service and the GraphQL gateway send.
	c := p.req.GetContext().AsMap()
	require.Equal(t, "member", c["user_role"])
	require.Equal(t, doc.WorkspaceID.String(), c["workspace_id"])
	require.Equal(t, doc.FolderID.String(), c["folder_id"])
	require.Equal(t, "active", c["lifecycle_state"])
	// Identity metadata the policy service's Tenant/UserIdentity
	// interceptors read; without it every check is Unauthenticated.
	require.Equal(t, []string{tenant.String()}, p.md.Get("x-tenant-id"))
	require.Equal(t, []string{user.String()}, p.md.Get("x-user-id"))
	require.Equal(t, []string{"member"}, p.md.Get("x-user-role"))
	require.Equal(t, []string{"carol@example.com"}, p.md.Get("x-user-name"))
}

func TestCanReadDocument_Denied(t *testing.T) {
	ok, err := canReadDocument(context.Background(), &recordingPolicy{allowed: false},
		uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), "member", testFacts())
	require.NoError(t, err)
	require.False(t, ok)
}

// Fail closed: no decision is an error, never "allowed" and never a
// silent "denied" that would read as "no tasks here".
func TestCanReadDocument_PolicyErrorFailsClosed(t *testing.T) {
	ok, err := canReadDocument(context.Background(), &recordingPolicy{err: errors.New("connection refused")},
		uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), "member", testFacts())
	require.Error(t, err)
	require.ErrorIs(t, err, ErrPolicyUnavailable)
	require.False(t, ok)
}

func TestCanReadDocument_NoPolicyClientFailsClosed(t *testing.T) {
	ok, err := canReadDocument(context.Background(), nil,
		uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), "member", testFacts())
	require.ErrorIs(t, err, ErrPolicyUnavailable)
	require.False(t, ok)
}
