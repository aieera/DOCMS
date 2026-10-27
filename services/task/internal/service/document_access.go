// I-2 (review of DEF-03) — document-scoped task listing follows document
// READ access. Product decision: whoever can read a document sees every
// task on it. The per-document Tasks panel (web DocumentTasksPanel) lists
// filter=all&document_id=X; DEF-03 had narrowed that to the caller's own
// tasks, so a member who could read X saw "No open tasks" while others'
// work on X was in flight.
//
// The read decision is the policy service's (synchronous gRPC — a
// hot-path permission check, like document → policy and storage →
// policy), asked with the same view request the GraphQL gateway sends for
// a document's permissions.
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/middleware"
	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
	"github.com/aieera/sedoc/services/task/internal/repository"
)

// PermissionChecker is the subset of the policy service's gRPC client the
// task service uses. Declared locally so tests can inject a fake (same
// shape as the storage service's PermissionChecker).
type PermissionChecker interface {
	CheckPermission(ctx context.Context, in *sedocv1.CheckPermissionRequest, opts ...grpc.CallOption) (*sedocv1.CheckPermissionResponse, error)
}

// ErrPolicyUnavailable wraps every failure to obtain a policy decision.
// Callers fail closed on it: an error to the client, never data and never
// a silent "denied" that would read as "nothing here".
var ErrPolicyUnavailable = errors.New("policy decision unavailable")

// policyCheckTimeout bounds one CheckPermission round-trip so a hung policy
// service cannot hold a list request open (the decision itself is a
// few-millisecond hot path).
const policyCheckTimeout = 5 * time.Second

// canReadDocument asks the policy service whether userID (tenant role
// `role`) may view the document described by doc. It sends what OPA's
// document rules need: the role (org-wide owner/admin rule), the
// workspace and folder (inheritance) and the lifecycle state (disposed
// deny) as context, and the tenant/user identity as gRPC metadata for the
// policy service's Tenant/UserIdentity interceptors. A nil client or a
// failed call returns ErrPolicyUnavailable.
func canReadDocument(ctx context.Context, policy PermissionChecker, tenantID, userID uuid.UUID, role string, doc repository.DocumentFacts) (bool, error) {
	if policy == nil {
		return false, fmt.Errorf("task service: %w: policy client not configured", ErrPolicyUnavailable)
	}
	pctx, err := structpb.NewStruct(map[string]any{
		"user_role":       role,
		"workspace_id":    doc.WorkspaceID.String(),
		"folder_id":       doc.FolderID.String(),
		"lifecycle_state": doc.LifecycleState,
	})
	if err != nil {
		return false, fmt.Errorf("task service: policy context: %w", err)
	}
	pairs := []string{
		middleware.TenantMetadataKey, tenantID.String(),
		middleware.UserMetadataKey, userID.String(),
		middleware.UserRoleMetadataKey, role,
	}
	if name := auth.GetUserName(ctx); name != "" {
		pairs = append(pairs, middleware.UserNameMetadataKey, name)
	}
	callCtx, cancel := context.WithTimeout(metadata.AppendToOutgoingContext(ctx, pairs...), policyCheckTimeout)
	defer cancel()

	resp, err := policy.CheckPermission(callCtx, &sedocv1.CheckPermissionRequest{
		SubjectType:  "user",
		SubjectId:    userID.String(),
		Action:       "view",
		ResourceType: "document",
		ResourceId:   doc.ID.String(),
		Context:      pctx,
	})
	if err != nil {
		return false, fmt.Errorf("task service: %w: %w", ErrPolicyUnavailable, err)
	}
	return resp.GetAllowed(), nil
}
