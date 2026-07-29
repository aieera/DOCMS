// Package resolver implements ADR 0074's read API by translating
// GraphQL field requests into upstream gRPC calls.
//
// The resolver is intentionally thin — it does not duplicate the
// REST handlers' validation, retry, or business rules. It:
//
//  1. Resolves the calling identity from the gateway-signed
//     X-Auth-Tenant-ID / X-Auth-User-ID headers (already in ctx
//     via pkg/middleware.Tenant + UserIdentity).
//  2. Calls policy.CheckPermission for the resource being accessed.
//     Denies degrade to nil (the executor renders that as JSON null).
//  3. Calls the upstream gRPC, batched via DataLoader where
//     multiple children share a parent.
//  4. Maps the proto into the model.* shape.
package resolver

import (
	"context"
	"errors"
	"time"

	"github.com/rs/zerolog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
	"github.com/aieera/sedoc/services/graphql-gateway/internal/loader"
	"github.com/aieera/sedoc/services/graphql-gateway/internal/model"
)

// Clients is the upstream gRPC fan-out the resolvers depend on.
// Any client may be nil at boot (graceful degradation): a resolver
// that needs a missing client returns ErrNotConfigured and the
// executor renders the field as null.
type Clients struct {
	Document      sedocv1.DocumentServiceClient
	Workflow      sedocv1.WorkflowServiceClient
	Collaboration sedocv1.CollaborationServiceClient
	Policy        sedocv1.PolicyServiceClient
	Audit         sedocv1.AuditServiceClient
}

// Resolver implements exec.Resolvers.
type Resolver struct {
	clients Clients
	log     zerolog.Logger
}

func New(clients Clients, log zerolog.Logger) *Resolver {
	return &Resolver{clients: clients, log: log}
}

// ErrNotConfigured is returned when the upstream gRPC client a
// resolver needs is nil. Wraps a sentinel so callers can errors.Is
// it for graceful degradation.
var ErrNotConfigured = errors.New("upstream service not configured")

// deniedOrMissing reports whether an upstream gRPC error means "you can't see
// this" (permission denied) or "it isn't there" (not found) — both of which a
// read resolver degrades to a null/empty field rather than surfacing as a hard
// GraphQL error.
func deniedOrMissing(err error) bool {
	c := status.Code(err)
	return c == codes.NotFound || c == codes.PermissionDenied
}

// ---- Identity helpers ----------------------------------------------

type identity struct {
	tenantID string
	userID   string
	role     string
}

func identityFrom(ctx context.Context) identity {
	tenant, _ := ctx.Value(ctxTenantKey{}).(string)
	user, _ := ctx.Value(ctxUserKey{}).(string)
	role, _ := ctx.Value(ctxRoleKey{}).(string)
	return identity{tenantID: tenant, userID: user, role: role}
}

type (
	ctxTenantKey struct{}
	ctxUserKey   struct{}
	ctxRoleKey   struct{}
)

// WithIdentity stamps the calling tenant + user + role onto the context.
// main.go calls this from the HTTP middleware after verifying the
// gateway-signed headers. The role feeds the policy check context so OPA
// Rule 6 (org owner/admin → all capabilities) can fire.
func WithIdentity(ctx context.Context, tenantID, userID, role string) context.Context {
	ctx = context.WithValue(ctx, ctxTenantKey{}, tenantID)
	ctx = context.WithValue(ctx, ctxUserKey{}, userID)
	ctx = context.WithValue(ctx, ctxRoleKey{}, role)
	return ctx
}

// outboundCtx attaches the gateway-trust headers to an outgoing
// gRPC call so the upstream's middleware.RequireGatewaySignature
// pass-through accepts it. Without this, every upstream call
// would fail with PermissionDenied.
func (r *Resolver) outboundCtx(ctx context.Context) context.Context {
	id := identityFrom(ctx)
	// Send the canonical gRPC metadata keys (x-tenant-id / x-user-id) that
	// middleware.TenantInterceptor + UserIdentityInterceptor actually read,
	// plus the x-auth-* aliases. Previously only x-auth-* were sent, so every
	// upstream gRPC call saw no tenant/user and 401'd with "authentication
	// required" — which is why the per-document Activity feed came back empty
	// (the policy "view" check failed before the audit query even ran).
	md := metadata.New(map[string]string{
		"x-tenant-id":      id.tenantID,
		"x-user-id":        id.userID,
		"x-user-role":      id.role,
		"x-auth-tenant-id": id.tenantID,
		"x-auth-user-id":   id.userID,
		"x-auth-user-role": id.role,
	})
	return metadata.NewOutgoingContext(ctx, md)
}

// ---- Permission gate -----------------------------------------------

func (r *Resolver) check(ctx context.Context, action, kind, resourceID string) bool {
	if r.clients.Policy == nil {
		// Fail-closed when policy is unreachable. Same posture as
		// services/document/cmd/server/main.go's denyAllPolicyClient.
		return false
	}
	id := identityFrom(ctx)
	// Pass the caller's role in the policy context so OPA Rule 6 (org
	// owner/admin → every capability) can fire. Without it even the tenant
	// owner is denied "view" on their own documents, so the per-document
	// Activity feed + document detail resolved empty. Mirrors the
	// extra["user_role"] the document service already sends.
	var pctx *structpb.Struct
	if id.role != "" {
		pctx, _ = structpb.NewStruct(map[string]any{"user_role": id.role})
	}
	resp, err := r.clients.Policy.CheckPermission(r.outboundCtx(ctx), &sedocv1.CheckPermissionRequest{
		SubjectType:  "user",
		SubjectId:    id.userID,
		Action:       action,
		ResourceType: kind,
		ResourceId:   resourceID,
		Context:      pctx,
	})
	if err != nil {
		r.log.Warn().Err(err).Str("kind", kind).Str("id", resourceID).
			Str("action", action).Msg("policy check failed; denying")
		return false
	}
	return resp.GetAllowed()
}

// ---- Time helpers --------------------------------------------------

func tsToTime(t *timestamppb.Timestamp) *time.Time {
	if t == nil || !t.IsValid() {
		return nil
	}
	out := t.AsTime()
	return &out
}

func tsToTimeOrZero(t *timestamppb.Timestamp) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.AsTime()
}

// ---- Document resolvers --------------------------------------------

func (r *Resolver) Document(ctx context.Context, id string) (any, error) {
	if r.clients.Document == nil {
		return nil, ErrNotConfigured
	}
	// Authority for "can view this document" is GetDocument itself — the
	// document service runs the policy check with FULL context (role +
	// workspace/folder cascade), which the gateway's own check() can't
	// replicate (it lacks the doc's workspace/folder, so it denied non-owner
	// workspace members). Not-found / permission-denied degrade to null.
	resp, err := r.clients.Document.GetDocument(r.outboundCtx(ctx), &sedocv1.GetDocumentRequest{DocumentId: id})
	if err != nil {
		if deniedOrMissing(err) {
			return nil, nil
		}
		return nil, err
	}
	return docToModel(resp), nil
}

func (r *Resolver) DocumentsByWorkspace(ctx context.Context, workspaceID string, limit int, cursor string) (any, error) {
	if r.clients.Document == nil {
		return nil, ErrNotConfigured
	}
	if !r.check(ctx, "view", "workspace", workspaceID) {
		return &model.Connection[*model.Document]{Nodes: []*model.Document{}}, nil
	}
	resp, err := r.clients.Document.ListDocuments(r.outboundCtx(ctx), &sedocv1.ListDocumentsRequest{
		WorkspaceId: workspaceID,
		Pagination:  &sedocv1.PaginationRequest{PageSize: int32(limit), PageToken: cursor},
	})
	if err != nil {
		return nil, err
	}
	out := make([]*model.Document, 0, len(resp.GetDocuments()))
	for _, d := range resp.GetDocuments() {
		out = append(out, docToModel(d))
	}
	return &model.Connection[*model.Document]{
		Nodes:      out,
		NextCursor: resp.GetPagination().GetNextPageToken(),
		TotalCount: int(resp.GetPagination().GetTotalCount()),
	}, nil
}

func docToModel(d *sedocv1.Document) *model.Document {
	// google.protobuf.Struct → map[string]any via AsMap(); a nil
	// Struct returns nil so the model's omitempty JSON tag keeps the
	// field absent rather than serializing a `null`.
	var meta map[string]any
	if s := d.GetCustomMetadata(); s != nil {
		meta = s.AsMap()
	}
	return &model.Document{
		ID:                       d.GetId(),
		TenantID:                 d.GetTenantId(),
		WorkspaceID:              d.GetWorkspaceId(),
		FolderID:                 d.GetFolderId(),
		Title:                    d.GetTitle(),
		Description:              d.GetDescription(),
		LifecycleState:           d.GetLifecycleState().String(),
		RegionPin:                d.GetRegionPin().String(),
		DocumentClass:            d.GetDocumentClass(),
		ClassificationConfidence: float64(d.GetClassificationConfidence()),
		Tags:                     d.GetTags(),
		MimeType:                 d.GetMimeType(),
		TotalSizeBytes:           d.GetTotalSizeBytes(),
		Sha256Hash:               d.GetSha256Hash(),
		CreatedBy:                d.GetCreatedBy(),
		CreatedAt:                tsToTime(d.GetCreatedAt()),
		UpdatedAt:                tsToTime(d.GetUpdatedAt()),
		CustomMetadata:           meta,
		CurrentVersionID:         d.GetCurrentVersionId(),
	}
}

// ---- Document child resolvers (DataLoader-batched) -----------------

func (r *Resolver) DocumentVersions(ctx context.Context, parent any, limit int, cursor string) (any, error) {
	doc, ok := parent.(*model.Document)
	if !ok || doc == nil {
		return &model.Connection[*model.Version]{}, nil
	}
	loaders := loader.FromContext(ctx)
	if loaders != nil && loaders.VersionsByDocument != nil && cursor == "" {
		// Cursor-paged calls bypass the loader (the loader keys on
		// document_id, not page token; mixing the two would either
		// require composite keys or drop pagination guarantees).
		v, err := loaders.VersionsByDocument.Load(ctx, doc.ID)
		if err != nil {
			return nil, err
		}
		if int(limit) > 0 && len(v) > int(limit) {
			v = v[:limit]
		}
		return &model.Connection[*model.Version]{Nodes: v}, nil
	}
	return r.fetchVersions(ctx, doc.ID, limit, cursor)
}

func (r *Resolver) fetchVersions(ctx context.Context, docID string, limit int, cursor string) (*model.Connection[*model.Version], error) {
	if r.clients.Document == nil {
		return &model.Connection[*model.Version]{}, nil
	}
	resp, err := r.clients.Document.ListVersions(r.outboundCtx(ctx), &sedocv1.ListVersionsRequest{
		DocumentId: docID,
		Pagination: &sedocv1.PaginationRequest{PageSize: int32(limit), PageToken: cursor},
	})
	if err != nil {
		return nil, err
	}
	out := make([]*model.Version, 0, len(resp.GetVersions()))
	for _, v := range resp.GetVersions() {
		out = append(out, versionToModel(v))
	}
	return &model.Connection[*model.Version]{
		Nodes:      out,
		NextCursor: resp.GetPagination().GetNextPageToken(),
	}, nil
}

func (r *Resolver) DocumentCurrentVersion(ctx context.Context, parent any) (any, error) {
	doc, ok := parent.(*model.Document)
	if !ok || doc == nil || doc.CurrentVersionID == "" {
		return nil, nil
	}
	// The version list resolver loader returns full lists; if we
	// have it cached, pick the matching id; otherwise fall back to
	// a single-version fetch via ListVersions(limit=1).
	loaders := loader.FromContext(ctx)
	if loaders != nil && loaders.VersionsByDocument != nil {
		if vs, err := loaders.VersionsByDocument.Load(ctx, doc.ID); err == nil {
			for _, v := range vs {
				if v.ID == doc.CurrentVersionID {
					return v, nil
				}
			}
		}
	}
	conn, err := r.fetchVersions(ctx, doc.ID, 1, "")
	if err != nil || len(conn.Nodes) == 0 {
		return nil, err
	}
	return conn.Nodes[0], nil
}

func versionToModel(v *sedocv1.Version) *model.Version {
	return &model.Version{
		ID:            v.GetId(),
		DocumentID:    v.GetDocumentId(),
		VersionNumber: v.GetVersionNumber(),
		ContentBlobID: v.GetContentBlobId(),
		SizeBytes:     v.GetSizeBytes(),
		MimeType:      v.GetMimeType(),
		Sha256Hash:    v.GetSha256Hash(),
		CreatedBy:     v.GetCreatedBy(),
		CreatedByName: v.GetCreatedByName(),
		CreatedAt:     tsToTime(v.GetCreatedAt()),
		ChangeSummary: v.GetChangeSummary(),
	}
}

func (r *Resolver) DocumentComments(ctx context.Context, parent any, includeResolved bool, limit int, cursor string) (any, error) {
	doc, ok := parent.(*model.Document)
	if !ok || doc == nil {
		return &model.Connection[*model.Comment]{}, nil
	}
	if r.clients.Collaboration == nil {
		return &model.Connection[*model.Comment]{}, nil
	}
	resp, err := r.clients.Collaboration.ListComments(r.outboundCtx(ctx), &sedocv1.ListCommentsRequest{
		DocumentId:      doc.ID,
		IncludeResolved: includeResolved,
		Page:            &sedocv1.PageRequest{PageSize: int32(limit), Cursor: cursor},
	})
	if err != nil {
		return nil, err
	}
	// Group by parent_id so the resolver can return the top-level
	// thread with replies attached. Comments without a parent are
	// the root nodes.
	byID := map[string]*model.Comment{}
	roots := make([]*model.Comment, 0)
	for _, c := range resp.GetComments() {
		m := commentToModel(c)
		byID[m.ID] = m
	}
	for _, c := range resp.GetComments() {
		m := byID[c.GetId()]
		if c.GetParentId() == "" {
			roots = append(roots, m)
			continue
		}
		if parent, ok := byID[c.GetParentId()]; ok {
			parent.Replies = append(parent.Replies, m)
		}
	}
	return &model.Connection[*model.Comment]{
		Nodes:      roots,
		NextCursor: resp.GetPage().GetNextCursor(),
	}, nil
}

func commentToModel(c *sedocv1.Comment) *model.Comment {
	out := &model.Comment{
		ID:              c.GetId(),
		DocumentID:      c.GetDocumentId(),
		ParentCommentID: c.GetParentId(),
		AuthorID:        c.GetAuthorId(),
		Body:            c.GetBody(),
		Resolved:        c.GetResolved(),
		ResolvedBy:      c.GetResolvedBy(),
		ResolvedAt:      tsToTime(c.GetResolvedAt()),
		CreatedAt:       tsToTimeOrZero(c.GetCreatedAt()),
		Replies:         []*model.Comment{},
	}
	return out
}

func (r *Resolver) CommentReplies(_ context.Context, parent any) (any, error) {
	c, ok := parent.(*model.Comment)
	if !ok || c == nil {
		return []*model.Comment{}, nil
	}
	return c.Replies, nil
}

func (r *Resolver) DocumentAnnotations(ctx context.Context, parent any, limit int, cursor string) (any, error) {
	doc, ok := parent.(*model.Document)
	if !ok || doc == nil {
		return &model.Connection[*model.Annotation]{}, nil
	}
	if r.clients.Collaboration == nil {
		return &model.Connection[*model.Annotation]{}, nil
	}
	resp, err := r.clients.Collaboration.ListAnnotations(r.outboundCtx(ctx), &sedocv1.ListAnnotationsRequest{
		DocumentId: doc.ID,
		PageReq:    &sedocv1.PageRequest{PageSize: int32(limit), Cursor: cursor},
	})
	if err != nil {
		return nil, err
	}
	out := make([]*model.Annotation, 0, len(resp.GetAnnotations()))
	for _, a := range resp.GetAnnotations() {
		out = append(out, &model.Annotation{
			ID:         a.GetId(),
			DocumentID: a.GetDocumentId(),
			AuthorID:   a.GetAuthorId(),
			Page:       a.GetPage(),
			Kind:       a.GetKind().String(),
			Payload:    a.GetGeometryJson(),
			CreatedAt:  tsToTimeOrZero(a.GetCreatedAt()),
			UpdatedAt:  tsToTime(a.GetUpdatedAt()),
		})
	}
	return &model.Connection[*model.Annotation]{
		Nodes:      out,
		NextCursor: resp.GetPage().GetNextCursor(),
	}, nil
}

func (r *Resolver) DocumentWorkflowInstances(ctx context.Context, parent any, limit int) (any, error) {
	doc, ok := parent.(*model.Document)
	if !ok || doc == nil {
		return []*model.WorkflowInstance{}, nil
	}
	if r.clients.Workflow == nil {
		return []*model.WorkflowInstance{}, nil
	}
	resp, err := r.clients.Workflow.ListInstances(r.outboundCtx(ctx), &sedocv1.ListInstancesRequest{
		DocumentId: doc.ID,
		Page:       &sedocv1.PageRequest{PageSize: int32(limit)},
	})
	if err != nil {
		return nil, err
	}
	out := make([]*model.WorkflowInstance, 0, len(resp.GetInstances()))
	for _, w := range resp.GetInstances() {
		out = append(out, workflowToModel(w))
	}
	return out, nil
}

func workflowToModel(w *sedocv1.WorkflowInstance) *model.WorkflowInstance {
	return &model.WorkflowInstance{
		ID:           w.GetId(),
		DefinitionID: w.GetDefinitionId(),
		DocumentID:   w.GetDocumentId(),
		Status:       w.GetStatus().String(),
		StartedAt:    tsToTimeOrZero(w.GetStartedAt()),
		CompletedAt:  tsToTime(w.GetCompletedAt()),
	}
}

func (r *Resolver) DocumentPermissions(ctx context.Context, parent any) (any, error) {
	doc, ok := parent.(*model.Document)
	if !ok || doc == nil || r.clients.Policy == nil {
		return &model.DocumentPermissions{}, nil
	}
	id := identityFrom(ctx)
	// Full policy context (role + workspace/folder cascade) so canView/... are
	// correct for owner/admin (OPA Rule 6) AND non-owner workspace members
	// (Rules 4/5a). The parent doc carries its workspace/folder, so unlike the
	// id-only resolvers this needs no extra fetch.
	cm := map[string]any{}
	if id.role != "" {
		cm["user_role"] = id.role
	}
	if doc.WorkspaceID != "" {
		cm["workspace_id"] = doc.WorkspaceID
	}
	if doc.FolderID != "" {
		cm["folder_id"] = doc.FolderID
	}
	pctx, _ := structpb.NewStruct(cm)
	checks := []*sedocv1.CheckPermissionRequest{
		{SubjectType: "user", SubjectId: id.userID, Action: "view", ResourceType: "document", ResourceId: doc.ID, Context: pctx},
		{SubjectType: "user", SubjectId: id.userID, Action: "edit", ResourceType: "document", ResourceId: doc.ID, Context: pctx},
		{SubjectType: "user", SubjectId: id.userID, Action: "delete", ResourceType: "document", ResourceId: doc.ID, Context: pctx},
		{SubjectType: "user", SubjectId: id.userID, Action: "share", ResourceType: "document", ResourceId: doc.ID, Context: pctx},
		{SubjectType: "user", SubjectId: id.userID, Action: "admin", ResourceType: "document", ResourceId: doc.ID, Context: pctx},
	}
	resp, err := r.clients.Policy.BatchCheckPermission(r.outboundCtx(ctx), &sedocv1.BatchCheckPermissionRequest{Checks: checks})
	if err != nil {
		return &model.DocumentPermissions{}, nil
	}
	results := resp.GetResults()
	get := func(i int) bool {
		if i >= len(results) {
			return false
		}
		return results[i].GetAllowed()
	}
	return &model.DocumentPermissions{
		CanView:   get(0),
		CanEdit:   get(1),
		CanDelete: get(2),
		CanShare:  get(3),
		CanAdmin:  get(4),
	}, nil
}

// ---- Workflow + Task resolvers -------------------------------------

func (r *Resolver) WorkflowInstance(ctx context.Context, id string) (any, error) {
	if r.clients.Workflow == nil {
		return nil, ErrNotConfigured
	}
	resp, err := r.clients.Workflow.GetInstance(r.outboundCtx(ctx), &sedocv1.GetInstanceRequest{Id: id})
	if err != nil {
		return nil, err
	}
	// SECURITY NOTE (Epic 8, TRACKED — defense-in-depth, not currently reachable):
	// a document-LESS instance (GetDocumentId()=="") skips the gateway view-check
	// and is returned to any tenant member who knows the id, relying entirely on
	// the workflow backend's own GetInstance authz. This top-level resolver is not
	// in the persisted-query manifest today, so there is no reachable path; if
	// workflowInstance is ever exposed, gate document-less instances on the
	// backend's per-user authz (or fail closed here). See
	// docs/security/epic8-gateway-followups.md.
	if resp.GetDocumentId() != "" && !r.check(ctx, "view", "document", resp.GetDocumentId()) {
		return nil, nil
	}
	return workflowToModel(resp), nil
}

func (r *Resolver) WorkflowInstancesByDocument(ctx context.Context, documentID string, limit int, cursor string) (any, error) {
	if !r.check(ctx, "view", "document", documentID) {
		return &model.Connection[*model.WorkflowInstance]{}, nil
	}
	if r.clients.Workflow == nil {
		return &model.Connection[*model.WorkflowInstance]{}, nil
	}
	resp, err := r.clients.Workflow.ListInstances(r.outboundCtx(ctx), &sedocv1.ListInstancesRequest{
		DocumentId: documentID,
		Page:       &sedocv1.PageRequest{PageSize: int32(limit), Cursor: cursor},
	})
	if err != nil {
		return nil, err
	}
	out := make([]*model.WorkflowInstance, 0, len(resp.GetInstances()))
	for _, w := range resp.GetInstances() {
		out = append(out, workflowToModel(w))
	}
	return &model.Connection[*model.WorkflowInstance]{
		Nodes:      out,
		NextCursor: resp.GetPage().GetNextCursor(),
	}, nil
}

func (r *Resolver) WorkflowInstanceTasks(ctx context.Context, parent any) (any, error) {
	w, ok := parent.(*model.WorkflowInstance)
	if !ok || w == nil || r.clients.Workflow == nil {
		return []*model.Task{}, nil
	}
	// ListTasks doesn't filter by instance directly today; we
	// pull everything assigned to the current user and intersect.
	// When a per-instance Tasks endpoint lands, swap to it.
	id := identityFrom(ctx)
	resp, err := r.clients.Workflow.ListTasks(r.outboundCtx(ctx), &sedocv1.ListTasksRequest{
		AssigneeId: id.userID,
		Page:       &sedocv1.PageRequest{PageSize: 100},
	})
	if err != nil {
		return []*model.Task{}, nil
	}
	out := make([]*model.Task, 0)
	for _, t := range resp.GetTasks() {
		if t.GetInstanceId() != w.ID {
			continue
		}
		out = append(out, taskToModel(t))
	}
	return out, nil
}

func (r *Resolver) WorkflowInstanceDocument(ctx context.Context, parent any) (any, error) {
	w, ok := parent.(*model.WorkflowInstance)
	if !ok || w == nil || w.DocumentID == "" {
		return nil, nil
	}
	return r.Document(ctx, w.DocumentID)
}

func (r *Resolver) TaskByID(ctx context.Context, id string) (any, error) {
	// Workflow service doesn't expose GetTask; the surface returns
	// it via ListTasks. For v1 we fan out and filter.
	if r.clients.Workflow == nil {
		return nil, ErrNotConfigured
	}
	cid := identityFrom(ctx)
	resp, err := r.clients.Workflow.ListTasks(r.outboundCtx(ctx), &sedocv1.ListTasksRequest{
		AssigneeId: cid.userID,
		Page:       &sedocv1.PageRequest{PageSize: 200},
	})
	if err != nil {
		return nil, err
	}
	for _, t := range resp.GetTasks() {
		if t.GetId() == id {
			return taskToModel(t), nil
		}
	}
	return nil, nil
}

func (r *Resolver) MyTasks(ctx context.Context, includeCompleted bool, limit int, cursor string) (any, error) {
	if r.clients.Workflow == nil {
		return &model.Connection[*model.Task]{}, nil
	}
	id := identityFrom(ctx)
	status := sedocv1.TaskStatus_TASK_STATUS_UNSPECIFIED
	if !includeCompleted {
		status = sedocv1.TaskStatus_TASK_STATUS_PENDING
	}
	resp, err := r.clients.Workflow.ListTasks(r.outboundCtx(ctx), &sedocv1.ListTasksRequest{
		AssigneeId: id.userID,
		Status:     status,
		Page:       &sedocv1.PageRequest{PageSize: int32(limit), Cursor: cursor},
	})
	if err != nil {
		return nil, err
	}
	out := make([]*model.Task, 0, len(resp.GetTasks()))
	for _, t := range resp.GetTasks() {
		out = append(out, taskToModel(t))
	}
	return &model.Connection[*model.Task]{
		Nodes:      out,
		NextCursor: resp.GetPage().GetNextCursor(),
	}, nil
}

func (r *Resolver) TaskWorkflowInstance(ctx context.Context, parent any) (any, error) {
	t, ok := parent.(*model.Task)
	if !ok || t == nil {
		return nil, nil
	}
	return r.WorkflowInstance(ctx, t.WorkflowInstanceID)
}

func taskToModel(t *sedocv1.Task) *model.Task {
	return &model.Task{
		ID:                 t.GetId(),
		WorkflowInstanceID: t.GetInstanceId(),
		AssigneeID:         t.GetAssigneeId(),
		Title:              t.GetTitle(),
		Status:             t.GetStatus().String(),
		DueAt:              tsToTime(t.GetDueAt()),
		CompletedAt:        tsToTime(t.GetCompletedAt()),
		CreatedAt:          time.Now(), // proto Task lacks created_at; placeholder
	}
}

// ---- Activity timeline ---------------------------------------------

func (r *Resolver) ActivityForDocument(ctx context.Context, documentID string, limit int, cursor string) (any, error) {
	// Gate on view access via GetDocument — the document service checks with
	// full context (role + workspace/folder cascade), so this works for
	// non-owner workspace members too, unlike the gateway's own check(). An
	// inaccessible or missing document yields an empty stream. (Falls back to
	// the gateway check() only when the document client is unavailable.)
	if r.clients.Document != nil {
		if _, err := r.clients.Document.GetDocument(r.outboundCtx(ctx),
			&sedocv1.GetDocumentRequest{DocumentId: documentID}); err != nil {
			return &model.Connection[*model.ActivityEvent]{}, nil
		}
	} else if !r.check(ctx, "view", "document", documentID) {
		return &model.Connection[*model.ActivityEvent]{}, nil
	}
	// v1: pull from the audit service when available; otherwise
	// return an empty stream so the field still resolves. The
	// audit service exposes a query endpoint over the same gRPC
	// trust headers; per-event JSON payload pass-through.
	if r.clients.Audit == nil {
		return &model.Connection[*model.ActivityEvent]{}, nil
	}
	// AuditService.Query returns events scoped by resource. We reuse
	// that for the activity feed; the GraphQL field is a view over
	// the audit stream filtered to one document.
	resp, err := r.clients.Audit.Query(r.outboundCtx(ctx), &sedocv1.QueryAuditRequest{
		ResourceKind: "document",
		ResourceId:   documentID,
		Page:         &sedocv1.PageRequest{PageSize: int32(limit), Cursor: cursor},
	})
	if err != nil {
		return nil, err
	}
	out := make([]*model.ActivityEvent, 0, len(resp.GetEvents()))
	for _, e := range resp.GetEvents() {
		out = append(out, &model.ActivityEvent{
			ID:         e.GetId(),
			DocumentID: documentID,
			Kind:       e.GetAction(),
			ActorID:    e.GetActorId(),
			OccurredAt: tsToTimeOrZero(e.GetOccurredAt()),
			Summary:    e.GetAction(), // human-readable mapping is a follow-up
			// metadata is map<string,string>; not JSON, but we marshal
			// it client-side for the timeline payload.
		})
	}
	return &model.Connection[*model.ActivityEvent]{
		Nodes:      out,
		NextCursor: resp.GetPage().GetNextCursor(),
	}, nil
}
