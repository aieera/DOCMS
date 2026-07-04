package handler

import (
	"context"

	"github.com/rs/zerolog"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/aieera/sedoc/pkg/auth"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/service"
)

// Handler implements sedocv1.DocumentServiceServer. It is a thin
// translation layer: every method maps proto -> domain input, calls
// service, and maps domain -> proto. Errors flow through ToGRPCError.
type Handler struct {
	sedocv1.UnimplementedDocumentServiceServer
	svc           *service.DocumentService
	log           zerolog.Logger
	publicBaseURL string // used to render share-link URLs
}

// New constructs a handler.
func New(svc *service.DocumentService, log zerolog.Logger, publicBaseURL string) *Handler {
	if publicBaseURL == "" {
		publicBaseURL = "/api/v1"
	}
	return &Handler{svc: svc, log: log, publicBaseURL: publicBaseURL}
}

// ---- Folders --------------------------------------------------------------

func (h *Handler) CreateFolder(ctx context.Context, req *sedocv1.CreateFolderRequest) (*sedocv1.Folder, error) {
	ws, err := parseUUID("workspace_id", req.GetWorkspaceId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	parent, err := parseUUIDOptional("parent_folder_id", req.GetParentFolderId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	f, err := h.svc.CreateFolder(ctx, &service.CreateFolderInput{
		WorkspaceID:    ws,
		Name:           req.GetName(),
		ParentFolderID: parent,
		Visibility:     model.FolderVisibility(req.GetVisibility()),
	})
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	return folderToProto(f), nil
}

func (h *Handler) GetFolder(ctx context.Context, req *sedocv1.GetFolderRequest) (*sedocv1.Folder, error) {
	id, err := parseUUID("folder_id", req.GetFolderId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	f, err := h.svc.GetFolder(ctx, id)
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	return folderToProto(f), nil
}

func (h *Handler) ListFolders(ctx context.Context, req *sedocv1.ListFoldersRequest) (*sedocv1.ListFoldersResponse, error) {
	ws, err := parseUUID("workspace_id", req.GetWorkspaceId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	parent, err := parseUUIDOptional("parent_folder_id", req.GetParentFolderId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	folders, nextToken, err := h.svc.ListFolders(ctx, ws, parent, req.GetPageToken(), int(req.GetPageSize()))
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	out := &sedocv1.ListFoldersResponse{
		Folders:       make([]*sedocv1.Folder, 0, len(folders)),
		NextPageToken: nextToken,
	}
	for i := range folders {
		out.Folders = append(out.Folders, folderToProto(&folders[i]))
	}
	return out, nil
}

func (h *Handler) UpdateFolder(ctx context.Context, req *sedocv1.UpdateFolderRequest) (*sedocv1.Folder, error) {
	id, err := parseUUID("folder_id", req.GetFolderId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	in := &service.UpdateFolderInput{FolderID: id}
	if req.GetName() != "" {
		name := req.GetName()
		in.Name = &name
	}
	if req.GetNewParentFolderId() != "" {
		p, err := parseUUID("new_parent_folder_id", req.GetNewParentFolderId())
		if err != nil {
			return nil, vdmserr.ToGRPCError(err)
		}
		in.NewParentFolderID = &p
	}
	f, err := h.svc.UpdateFolder(ctx, in)
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	return folderToProto(f), nil
}

func (h *Handler) DeleteFolder(ctx context.Context, req *sedocv1.DeleteFolderRequest) (*emptypb.Empty, error) {
	id, err := parseUUID("folder_id", req.GetFolderId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	if err := h.svc.DeleteFolder(ctx, id); err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	return &emptypb.Empty{}, nil
}

// SetFolderVisibility flips a folder's visibility between shared and
// private. Visibility = ” returns InvalidArgument. The service layer
// enforces owner / admin gating.
func (h *Handler) SetFolderVisibility(ctx context.Context, req *sedocv1.SetFolderVisibilityRequest) (*sedocv1.Folder, error) {
	id, err := parseUUID("folder_id", req.GetFolderId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	f, err := h.svc.SetFolderVisibility(ctx, &service.SetFolderVisibilityInput{
		FolderID:   id,
		Visibility: model.FolderVisibility(req.GetVisibility()),
	})
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	return folderToProto(f), nil
}

func (h *Handler) ListFolderGrants(ctx context.Context, req *sedocv1.ListFolderGrantsRequest) (*sedocv1.ListFolderGrantsResponse, error) {
	id, err := parseUUID("folder_id", req.GetFolderId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	grants, err := h.svc.ListFolderGrants(ctx, id)
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	out := &sedocv1.ListFolderGrantsResponse{Grants: make([]*sedocv1.FolderGrant, 0, len(grants))}
	for i := range grants {
		out.Grants = append(out.Grants, folderGrantToProto(&grants[i]))
	}
	return out, nil
}

func (h *Handler) AddFolderGrant(ctx context.Context, req *sedocv1.AddFolderGrantRequest) (*sedocv1.FolderGrant, error) {
	id, err := parseUUID("folder_id", req.GetFolderId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	grantee, err := parseUUID("grantee_id", req.GetGranteeId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	g, err := h.svc.AddFolderGrant(ctx, &service.AddFolderGrantInput{
		FolderID:    id,
		GranteeType: req.GetGranteeType(),
		GranteeID:   grantee,
	})
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	return folderGrantToProto(g), nil
}

func (h *Handler) RemoveFolderGrant(ctx context.Context, req *sedocv1.RemoveFolderGrantRequest) (*emptypb.Empty, error) {
	id, err := parseUUID("folder_id", req.GetFolderId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	grantee, err := parseUUID("grantee_id", req.GetGranteeId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	if err := h.svc.RemoveFolderGrant(ctx, id, req.GetGranteeType(), grantee); err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	return &emptypb.Empty{}, nil
}

// ---- Documents ------------------------------------------------------------

func (h *Handler) CreateDocument(ctx context.Context, req *sedocv1.CreateDocumentRequest) (*sedocv1.Document, error) {
	ws, err := parseUUID("workspace_id", req.GetWorkspaceId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	folder, err := parseUUID("folder_id", req.GetFolderId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	userID, _ := auth.GetUserID(ctx)
	d, err := h.svc.CreateDocument(ctx, &service.CreateDocumentInput{
		WorkspaceID:    ws,
		FolderID:       folder,
		Title:          req.GetTitle(),
		Description:    req.GetDescription(),
		RegionPin:      regionToString(req.GetRegionPin()),
		CustomMetadata: structToMap(req.GetCustomMetadata()),
		Tags:           req.GetTags(),
		ExternalID:     req.GetExternalId(),
		UpdatedBy:      userID,
	})
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	return documentToProto(d, nil), nil
}

func (h *Handler) GetDocument(ctx context.Context, req *sedocv1.GetDocumentRequest) (*sedocv1.Document, error) {
	id, err := parseUUID("document_id", req.GetDocumentId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	d, p, err := h.svc.GetDocument(ctx, id)
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	return documentToProto(d, p), nil
}

func (h *Handler) UpdateDocument(ctx context.Context, req *sedocv1.UpdateDocumentRequest) (*sedocv1.Document, error) {
	id, err := parseUUID("document_id", req.GetDocumentId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	userID, _ := auth.GetUserID(ctx)
	in := &service.UpdateDocumentInput{DocumentID: id, ClearTags: req.GetClearTags(), UpdatedBy: userID}
	if req.Title != "" {
		t := req.Title
		in.Title = &t
	}
	if req.Description != "" {
		d := req.Description
		in.Description = &d
	}
	if req.GetCustomMetadata() != nil {
		in.CustomMetadata = structToMap(req.GetCustomMetadata())
	}
	if len(req.GetTags()) > 0 {
		in.Tags = req.GetTags()
	}
	d, err := h.svc.UpdateDocument(ctx, in)
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	return documentToProto(d, nil), nil
}

func (h *Handler) DeleteDocument(ctx context.Context, req *sedocv1.DeleteDocumentRequest) (*emptypb.Empty, error) {
	id, err := parseUUID("document_id", req.GetDocumentId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	if err := h.svc.DeleteDocument(ctx, id); err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	return &emptypb.Empty{}, nil
}

func (h *Handler) MoveDocument(ctx context.Context, req *sedocv1.MoveDocumentRequest) (*sedocv1.Document, error) {
	id, err := parseUUID("document_id", req.GetDocumentId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	target, err := parseUUID("target_folder_id", req.GetTargetFolderId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	targetWS, err := parseUUIDOptional("target_workspace_id", req.GetTargetWorkspaceId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	userID, _ := auth.GetUserID(ctx)
	d, err := h.svc.MoveDocument(ctx, &service.MoveDocumentInput{
		DocumentID:        id,
		TargetFolderID:    target,
		TargetWorkspaceID: targetWS,
		UpdatedBy:         userID,
	})
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	return documentToProto(d, nil), nil
}

func (h *Handler) CopyDocument(ctx context.Context, req *sedocv1.CopyDocumentRequest) (*sedocv1.Document, error) {
	id, err := parseUUID("document_id", req.GetDocumentId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	target, err := parseUUID("target_folder_id", req.GetTargetFolderId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	targetWS, err := parseUUIDOptional("target_workspace_id", req.GetTargetWorkspaceId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	userID, _ := auth.GetUserID(ctx)
	d, err := h.svc.CopyDocument(ctx, &service.CopyDocumentInput{
		DocumentID:        id,
		TargetFolderID:    target,
		TargetWorkspaceID: targetWS,
		CopiedBy:          userID,
	})
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	return documentToProto(d, nil), nil
}

func (h *Handler) ListDocuments(ctx context.Context, req *sedocv1.ListDocumentsRequest) (*sedocv1.ListDocumentsResponse, error) {
	ws, err := parseUUID("workspace_id", req.GetWorkspaceId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	f := model.DocumentFilter{
		WorkspaceID:    &ws,
		DocumentClass:  req.GetDocumentClass(),
		Tags:           req.GetTags(),
		Query:          req.GetQuery(),
		SortBy:         req.GetSortBy(),
		SortOrder:      sortOrderFromProto(req.GetSortOrder()),
		PageSize:       int(req.GetPagination().GetPageSize()),
		PageToken:      req.GetPagination().GetPageToken(),
		IncludeDeleted: req.GetIncludeDeleted(),
	}
	if req.GetFolderId() != "" {
		folder, err := parseUUID("folder_id", req.GetFolderId())
		if err != nil {
			return nil, vdmserr.ToGRPCError(err)
		}
		f.FolderID = &folder
	}
	if ls := lifecycleStateFromProto(req.GetLifecycleState()); ls != nil {
		f.LifecycleState = ls
	}
	if t := req.GetCreatedAfter(); t != nil {
		tt := t.AsTime()
		f.CreatedAfter = &tt
	}
	if t := req.GetCreatedBefore(); t != nil {
		tt := t.AsTime()
		f.CreatedBefore = &tt
	}

	page, err := h.svc.ListDocuments(ctx, f)
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}

	out := &sedocv1.ListDocumentsResponse{
		Documents: make([]*sedocv1.Document, 0, len(page.Items)),
		Pagination: &sedocv1.PaginationResponse{
			NextPageToken: page.NextPageToken,
			TotalCount:    page.TotalCount,
		},
	}
	for i := range page.Items {
		out.Documents = append(out.Documents, documentToProto(&page.Items[i], nil))
	}
	return out, nil
}

// ---- Versions -------------------------------------------------------------

func (h *Handler) CreateVersion(ctx context.Context, req *sedocv1.CreateVersionRequest) (*sedocv1.Version, error) {
	doc, err := parseUUID("document_id", req.GetDocumentId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	blob, err := parseUUID("content_blob_id", req.GetContentBlobId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	in := &service.CreateVersionInput{
		DocumentID:    doc,
		ContentBlobID: blob,
		ChangeSummary: req.GetChangeSummary(),
	}
	if bv := req.GetBaseVersionId(); bv != "" {
		base, perr := parseUUID("base_version_id", bv)
		if perr != nil {
			return nil, vdmserr.ToGRPCError(perr)
		}
		in.BaseVersionID = &base
	}
	v, err := h.svc.CreateVersion(ctx, in)
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	return versionToProto(v), nil
}

func (h *Handler) RestoreVersion(ctx context.Context, req *sedocv1.RestoreVersionRequest) (*sedocv1.Version, error) {
	doc, err := parseUUID("document_id", req.GetDocumentId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	ver, err := parseUUID("version_id", req.GetVersionId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	v, err := h.svc.RestoreVersion(ctx, doc, ver, req.GetNote())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	return versionToProto(v), nil
}

func (h *Handler) ListVersions(ctx context.Context, req *sedocv1.ListVersionsRequest) (*sedocv1.ListVersionsResponse, error) {
	doc, err := parseUUID("document_id", req.GetDocumentId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	page, err := h.svc.ListVersions(ctx, doc, int(req.GetPagination().GetPageSize()), req.GetPagination().GetPageToken())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	out := &sedocv1.ListVersionsResponse{
		Versions: make([]*sedocv1.Version, 0, len(page.Items)),
		Pagination: &sedocv1.PaginationResponse{
			NextPageToken: page.NextPageToken,
			TotalCount:    page.TotalCount,
		},
	}
	for i := range page.Items {
		out.Versions = append(out.Versions, versionToProto(&page.Items[i]))
	}
	return out, nil
}

// ---- Lifecycle ------------------------------------------------------------

func (h *Handler) UpdateLifecycle(ctx context.Context, req *sedocv1.UpdateLifecycleRequest) (*sedocv1.Document, error) {
	id, err := parseUUID("document_id", req.GetDocumentId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	action, err := lifecycleActionFromProto(req.GetAction())
	if err != nil {
		return nil, vdmserr.ToGRPCError(vdmserr.Validation("action", err.Error()))
	}
	d, err := h.svc.UpdateLifecycle(ctx, &service.UpdateLifecycleInput{
		DocumentID:          id,
		Action:              action,
		Reason:              req.GetReason(),
		HoldName:            req.GetHoldName(),
		HoldMatterReference: req.GetHoldMatterReference(),
	})
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	return documentToProto(d, nil), nil
}

// ---- Share links ----------------------------------------------------------

func (h *Handler) CreateShareLink(ctx context.Context, req *sedocv1.CreateShareLinkRequest) (*sedocv1.ShareLink, error) {
	doc, err := parseUUID("document_id", req.GetDocumentId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	perms := req.GetPermissions()
	if len(perms) == 0 {
		perms = []string{"view"}
	}
	l, err := h.svc.CreateShareLink(ctx, &service.CreateShareLinkInput{
		DocumentID:     doc,
		Password:       req.GetPassword(),
		ExpiresInHours: int(req.GetExpiresInHours()),
		MaxViews:       int(req.GetMaxViews()),
		Permissions:    perms,
	})
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	return shareLinkToProto(l, h.publicBaseURL), nil
}

func (h *Handler) ListShareLinks(ctx context.Context, req *sedocv1.ListShareLinksRequest) (*sedocv1.ListShareLinksResponse, error) {
	doc, err := parseUUID("document_id", req.GetDocumentId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	links, err := h.svc.ListShareLinks(ctx, doc)
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	out := &sedocv1.ListShareLinksResponse{ShareLinks: make([]*sedocv1.ShareLink, 0, len(links))}
	for i := range links {
		out.ShareLinks = append(out.ShareLinks, shareLinkToProto(&links[i], h.publicBaseURL))
	}
	return out, nil
}

func (h *Handler) DeleteShareLink(ctx context.Context, req *sedocv1.DeleteShareLinkRequest) (*emptypb.Empty, error) {
	id, err := parseUUID("share_link_id", req.GetShareLinkId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	if err := h.svc.DeleteShareLink(ctx, id); err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	return &emptypb.Empty{}, nil
}

func (h *Handler) AccessShareLink(ctx context.Context, req *sedocv1.AccessShareLinkRequest) (*sedocv1.AccessShareLinkResponse, error) {
	res, err := h.svc.AccessShareLink(ctx, &service.AccessShareLinkInput{
		Token:    req.GetToken(),
		Password: req.GetPassword(),
	})
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	out := &sedocv1.AccessShareLinkResponse{PasswordRequired: res.PasswordRequired}
	if res.Document != nil {
		out.Document = documentToProto(res.Document, nil)
	}
	out.DownloadUrl = res.DownloadURL
	return out, nil
}

// ---- Tags -----------------------------------------------------------------

func (h *Handler) CreateTag(ctx context.Context, req *sedocv1.CreateTagRequest) (*sedocv1.Tag, error) {
	t, err := h.svc.CreateTag(ctx, &service.CreateTagInput{
		Name:  req.GetName(),
		Color: req.GetColor(),
	})
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	return tagToProto(t), nil
}

func (h *Handler) ListTags(ctx context.Context, _ *sedocv1.ListTagsRequest) (*sedocv1.ListTagsResponse, error) {
	tags, err := h.svc.ListTags(ctx)
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	out := &sedocv1.ListTagsResponse{Tags: make([]*sedocv1.Tag, 0, len(tags))}
	for i := range tags {
		out.Tags = append(out.Tags, tagToProto(&tags[i]))
	}
	return out, nil
}

func (h *Handler) DeleteTag(ctx context.Context, req *sedocv1.DeleteTagRequest) (*emptypb.Empty, error) {
	id, err := parseUUID("tag_id", req.GetTagId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	if err := h.svc.DeleteTag(ctx, id); err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	return &emptypb.Empty{}, nil
}

// ---- Batch ----------------------------------------------------------------

func (h *Handler) BatchUpdateMetadata(ctx context.Context, req *sedocv1.BatchUpdateMetadataRequest) (*sedocv1.BatchUpdateMetadataResponse, error) {
	in := &service.BatchUpdateMetadataInput{
		MetadataUpdates: structToMap(req.GetMetadataUpdates()),
	}
	for _, raw := range req.GetDocumentIds() {
		id, err := parseUUID("document_ids", raw)
		if err != nil {
			return nil, vdmserr.ToGRPCError(err)
		}
		in.DocumentIDs = append(in.DocumentIDs, id)
	}

	res, err := h.svc.BatchUpdateMetadata(ctx, in)
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	out := &sedocv1.BatchUpdateMetadataResponse{
		UpdatedCount:      int32(res.UpdatedCount),
		FailureReasons:    res.FailureReasons,
		FailedDocumentIds: make([]string, 0, len(res.FailedDocumentIDs)),
	}
	for _, fid := range res.FailedDocumentIDs {
		out.FailedDocumentIds = append(out.FailedDocumentIds, fid.String())
	}
	return out, nil
}

// ---- Metadata schema ------------------------------------------------------

func (h *Handler) GetMetadataSchema(ctx context.Context, _ *sedocv1.GetMetadataSchemaRequest) (*sedocv1.GetMetadataSchemaResponse, error) {
	m, err := h.svc.GetMetadataSchema(ctx)
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	s, err := mapToStruct(m)
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	return &sedocv1.GetMetadataSchemaResponse{JsonSchema: s}, nil
}

func (h *Handler) UpdateMetadataSchema(ctx context.Context, req *sedocv1.UpdateMetadataSchemaRequest) (*sedocv1.GetMetadataSchemaResponse, error) {
	m, err := h.svc.UpdateMetadataSchema(ctx, structToMap(req.GetJsonSchema()))
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	s, err := mapToStruct(m)
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	return &sedocv1.GetMetadataSchemaResponse{JsonSchema: s}, nil
}
