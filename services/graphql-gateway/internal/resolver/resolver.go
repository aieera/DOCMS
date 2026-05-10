// Package resolver implements ADR 0074's read API by translating
// GraphQL field requests into upstream gRPC calls.
//
// The resolver is intentionally thin — it does not duplicate the
// REST handlers' validation, retry, or business rules. It:
//
//   1. Resolves the calling identity from the gateway-signed
//      X-Auth-Tenant-ID / X-Auth-User-ID headers (already in ctx
//      via pkg/middleware.Tenant + UserIdentity).
//   2. Calls policy.CheckPermission for the resource being accessed.
//      Denies degrade to nil (the executor renders that as JSON null).
//   3. Calls the upstream gRPC, batched via DataLoader where
//      multiple children share a parent.
//   4. Maps the proto into the model.* shape.
package resolver

import (
	"context"
	"errors"
	"time"

	"github.com/rs/zerolog"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/timestamppb"

	vaultdmsv1 "github.com/vaultdms/vaultdms/proto/gen/go/vaultdms/v1"
	"github.com/vaultdms/vaultdms/services/graphql-gateway/internal/loader"
	"github.com/vaultdms/vaultdms/services/graphql-gateway/internal/model"
)

// Clients is the upstream gRPC fan-out the resolvers depend on.
// Any client may be nil at boot (graceful degradation): a resolver
// that needs a missing client returns ErrNotConfigured and the
// executor renders the field as null.
type Clients struct {
	Document      vaultdmsv1.DocumentServiceClient
	Workflow      vaultdmsv1.WorkflowServiceClient
	Collaboration vaultdmsv1.CollaborationServiceClient
	Policy        vaultdmsv1.PolicyServiceClient
	Audit         vaultdmsv1.AuditServiceClient
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

// ---- Identity helpers ----------------------------------------------

type identity struct {
	tenantID string
	userID   string
}

func identityFrom(ctx context.Context) identity {
	tenant, _ := ctx.Value(ctxTenantKey{}).(string)
	user, _ := ctx.Value(ctxUserKey{}).(string)
	return identity{tenantID: tenant, userID: user}
}

type (
	ctxTenantKey struct{}
	ctxUserKey   struct{}
)

// WithIdentity stamps the calling tenant + user onto the context.
// main.go calls this from the HTTP middleware after verifying the
// gateway-signed headers.
func WithIdentity(ctx context.Context, tenantID, userID string) context.Context {
	ctx = context.WithValue(ctx, ctxTenantKey{}, tenantID)
	ctx = context.WithValue(ctx, ctxUserKey{}, userID)
	return ctx
}

// outboundCtx attaches the gateway-trust headers to an outgoing
// gRPC call so the upstream's middleware.RequireGatewaySignature
// pass-through accepts it. Without this, every upstream call
// would fail with PermissionDenied.
func (r *Resolver) outboundCtx(ctx context.Context) context.Context {
	id := identityFrom(ctx)
	md := metadata.New(map[string]string{
		"x-auth-tenant-id": id.tenantID,
		"x-auth-user-id":   id.userID,
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
	resp, err := r.clients.Policy.CheckPermission(r.outboundCtx(ctx), &vaultdmsv1.CheckPermissionRequest{
		SubjectType:  "user",
		SubjectId:    id.userID,
		Action:       action,
		ResourceType: kind,
		ResourceId:   resourceID,
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
	if !r.check(ctx, "view", "document", id) {
		return nil, nil
	}
	resp, err := r.clients.Document.GetDocument(r.outboundCtx(ctx), &vaultdmsv1.GetDocumentRequest{DocumentId: id})
	if err != nil {
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
	resp, err := r.clients.Document.ListDocuments(r.outboundCtx(ctx), &vaultdmsv1.ListDocumentsRequest{
		WorkspaceId: workspaceID,
		Pagination:  &vaultdmsv1.PaginationRequest{PageSize: int32(limit), PageToken: cursor},
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

func docToModel(d *vaultdmsv1.Document) *model.Document {
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
	resp, err := r.clients.Document.ListVersions(r.outboundCtx(ctx), &vaultdmsv1.ListVersionsRequest{
		DocumentId: docID,
		Pagination: &vaultdmsv1.PaginationRequest{PageSize: int32(limit), PageToken: cursor},
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

func versionToModel(v *vaultdmsv1.Version) *model.Version {
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
	resp, err := r.clients.Collaboration.ListComments(r.outboundCtx(ctx), &vaultdmsv1.ListCommentsRequest{
		DocumentId:      doc.ID,
		IncludeResolved: includeResolved,
		Page:            &vaultdmsv1.PageRequest{PageSize: int32(limit), Cursor: cursor},
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

func commentToModel(c *vaultdmsv1.Comment) *model.Comment {
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
	resp, err := r.clients.Collaboration.ListAnnotations(r.outboundCtx(ctx), &vaultdmsv1.ListAnnotationsRequest{
		DocumentId: doc.ID,
		PageReq:    &vaultdmsv1.PageRequest{PageSize: int32(limit), Cursor: cursor},
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
	resp, err := r.clients.Workflow.ListInstances(r.outboundCtx(ctx), &vaultdmsv1.ListInstancesRequest{
		DocumentId: doc.ID,
		Page:       &vaultdmsv1.PageRequest{PageSize: int32(limit)},
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

func workflowToModel(w *vaultdmsv1.WorkflowInstance) *model.WorkflowInstance {
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
	checks := []*vaultdmsv1.CheckPermissionRequest{
		{SubjectType: "user", SubjectId: id.userID, Action: "view", ResourceType: "document", ResourceId: doc.ID},
		{SubjectType: "user", SubjectId: id.userID, Action: "edit", ResourceType: "document", ResourceId: doc.ID},
		{SubjectType: "user", SubjectId: id.userID, Action: "delete", ResourceType: "document", ResourceId: doc.ID},
		{SubjectType: "user", SubjectId: id.userID, Action: "share", ResourceType: "document", ResourceId: doc.ID},
		{SubjectType: "user", SubjectId: id.userID, Action: "admin", ResourceType: "document", ResourceId: doc.ID},
	}
	resp, err := r.clients.Policy.BatchCheckPermission(r.outboundCtx(ctx), &vaultdmsv1.BatchCheckPermissionRequest{Checks: checks})
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
	resp, err := r.clients.Workflow.GetInstance(r.outboundCtx(ctx), &vaultdmsv1.GetInstanceRequest{Id: id})
	if err != nil {
		return nil, err
	}
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
	resp, err := r.clients.Workflow.ListInstances(r.outboundCtx(ctx), &vaultdmsv1.ListInstancesRequest{
		DocumentId: documentID,
		Page:       &vaultdmsv1.PageRequest{PageSize: int32(limit), Cursor: cursor},
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
	resp, err := r.clients.Workflow.ListTasks(r.outboundCtx(ctx), &vaultdmsv1.ListTasksRequest{
		AssigneeId: id.userID,
		Page:       &vaultdmsv1.PageRequest{PageSize: 100},
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
	resp, err := r.clients.Workflow.ListTasks(r.outboundCtx(ctx), &vaultdmsv1.ListTasksRequest{
		AssigneeId: cid.userID,
		Page:       &vaultdmsv1.PageRequest{PageSize: 200},
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
	status := vaultdmsv1.TaskStatus_TASK_STATUS_UNSPECIFIED
	if !includeCompleted {
		status = vaultdmsv1.TaskStatus_TASK_STATUS_PENDING
	}
	resp, err := r.clients.Workflow.ListTasks(r.outboundCtx(ctx), &vaultdmsv1.ListTasksRequest{
		AssigneeId: id.userID,
		Status:     status,
		Page:       &vaultdmsv1.PageRequest{PageSize: int32(limit), Cursor: cursor},
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

func taskToModel(t *vaultdmsv1.Task) *model.Task {
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
	if !r.check(ctx, "view", "document", documentID) {
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
	resp, err := r.clients.Audit.Query(r.outboundCtx(ctx), &vaultdmsv1.QueryAuditRequest{
		ResourceKind: "document",
		ResourceId:   documentID,
		Page:         &vaultdmsv1.PageRequest{PageSize: int32(limit), Cursor: cursor},
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
