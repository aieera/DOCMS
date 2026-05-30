// Package handler exposes the gRPC DocumentService surface and translates
// between proto types and the internal domain model.
package handler

import (
	"encoding/json"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	vaultdmsv1 "github.com/vaultdms/vaultdms/proto/gen/go/vaultdms/v1"
	"github.com/vaultdms/vaultdms/services/document/internal/model"
	"github.com/vaultdms/vaultdms/services/document/internal/service"
)

// ---- UUID helpers ---------------------------------------------------------

// parseUUID returns a typed validation error for malformed or empty ids, so
// handlers can surface clean 400 responses.
func parseUUID(field, s string) (uuid.UUID, error) {
	if s == "" {
		return uuid.Nil, vdmserr.Validation(field, "required")
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, vdmserr.Validation(field, "not a valid UUID")
	}
	return id, nil
}

// parseUUIDOptional returns (nil, nil) for empty input and a parsed UUID
// pointer otherwise.
func parseUUIDOptional(field, s string) (*uuid.UUID, error) {
	if s == "" {
		return nil, nil
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return nil, vdmserr.Validation(field, "not a valid UUID")
	}
	return &id, nil
}

// ---- Struct <-> map -------------------------------------------------------

func structToMap(s *structpb.Struct) map[string]any {
	if s == nil {
		return nil
	}
	return s.AsMap()
}

func mapToStruct(m map[string]any) (*structpb.Struct, error) {
	if m == nil {
		return nil, nil
	}
	return structpb.NewStruct(m)
}

// ---- Region / lifecycle enums --------------------------------------------

func regionToString(r vaultdmsv1.RegionPin) string {
	switch r {
	case vaultdmsv1.RegionPin_REGION_PIN_US_EAST_1:
		return "us-east-1"
	case vaultdmsv1.RegionPin_REGION_PIN_EU_WEST_1:
		return "eu-west-1"
	case vaultdmsv1.RegionPin_REGION_PIN_ME_SOUTH_1:
		return "me-south-1"
	case vaultdmsv1.RegionPin_REGION_PIN_AP_SOUTHEAST_1:
		return "ap-southeast-1"
	}
	return ""
}

func regionToProto(r string) vaultdmsv1.RegionPin {
	switch r {
	case "us-east-1":
		return vaultdmsv1.RegionPin_REGION_PIN_US_EAST_1
	case "eu-west-1":
		return vaultdmsv1.RegionPin_REGION_PIN_EU_WEST_1
	case "me-south-1":
		return vaultdmsv1.RegionPin_REGION_PIN_ME_SOUTH_1
	case "ap-southeast-1":
		return vaultdmsv1.RegionPin_REGION_PIN_AP_SOUTHEAST_1
	}
	return vaultdmsv1.RegionPin_REGION_PIN_UNSPECIFIED
}

func lifecycleStateToProto(s model.LifecycleState) vaultdmsv1.LifecycleState {
	switch s {
	case model.StateDraft:
		return vaultdmsv1.LifecycleState_LIFECYCLE_STATE_DRAFT
	case model.StateInReview:
		return vaultdmsv1.LifecycleState_LIFECYCLE_STATE_IN_REVIEW
	case model.StateActive:
		return vaultdmsv1.LifecycleState_LIFECYCLE_STATE_ACTIVE
	case model.StateSuperseded:
		return vaultdmsv1.LifecycleState_LIFECYCLE_STATE_SUPERSEDED
	case model.StateRetained:
		return vaultdmsv1.LifecycleState_LIFECYCLE_STATE_RETAINED
	case model.StateArchived:
		return vaultdmsv1.LifecycleState_LIFECYCLE_STATE_ARCHIVED
	case model.StateDisposed:
		return vaultdmsv1.LifecycleState_LIFECYCLE_STATE_DISPOSED
	case model.StateLegalHold:
		return vaultdmsv1.LifecycleState_LIFECYCLE_STATE_LEGAL_HOLD
	}
	return vaultdmsv1.LifecycleState_LIFECYCLE_STATE_UNSPECIFIED
}

func lifecycleStateFromProto(s vaultdmsv1.LifecycleState) *model.LifecycleState {
	var out model.LifecycleState
	switch s {
	case vaultdmsv1.LifecycleState_LIFECYCLE_STATE_DRAFT:
		out = model.StateDraft
	case vaultdmsv1.LifecycleState_LIFECYCLE_STATE_IN_REVIEW:
		out = model.StateInReview
	case vaultdmsv1.LifecycleState_LIFECYCLE_STATE_ACTIVE:
		out = model.StateActive
	case vaultdmsv1.LifecycleState_LIFECYCLE_STATE_SUPERSEDED:
		out = model.StateSuperseded
	case vaultdmsv1.LifecycleState_LIFECYCLE_STATE_RETAINED:
		out = model.StateRetained
	case vaultdmsv1.LifecycleState_LIFECYCLE_STATE_ARCHIVED:
		out = model.StateArchived
	case vaultdmsv1.LifecycleState_LIFECYCLE_STATE_DISPOSED:
		out = model.StateDisposed
	case vaultdmsv1.LifecycleState_LIFECYCLE_STATE_LEGAL_HOLD:
		out = model.StateLegalHold
	default:
		return nil
	}
	return &out
}

func lifecycleActionFromProto(a vaultdmsv1.LifecycleAction) (model.LifecycleAction, error) {
	switch a {
	case vaultdmsv1.LifecycleAction_LIFECYCLE_ACTION_SUBMIT_FOR_REVIEW:
		return model.ActionSubmitForReview, nil
	case vaultdmsv1.LifecycleAction_LIFECYCLE_ACTION_APPROVE:
		return model.ActionApprove, nil
	case vaultdmsv1.LifecycleAction_LIFECYCLE_ACTION_REJECT:
		return model.ActionReject, nil
	case vaultdmsv1.LifecycleAction_LIFECYCLE_ACTION_SUPERSEDE:
		return model.ActionSupersede, nil
	case vaultdmsv1.LifecycleAction_LIFECYCLE_ACTION_ARCHIVE:
		return model.ActionArchive, nil
	case vaultdmsv1.LifecycleAction_LIFECYCLE_ACTION_DISPOSE:
		return model.ActionDispose, nil
	case vaultdmsv1.LifecycleAction_LIFECYCLE_ACTION_APPLY_HOLD:
		return model.ActionApplyHold, nil
	case vaultdmsv1.LifecycleAction_LIFECYCLE_ACTION_RELEASE_HOLD:
		return model.ActionReleaseHold, nil
	case vaultdmsv1.LifecycleAction_LIFECYCLE_ACTION_RESTORE:
		return model.ActionRestore, nil
	}
	return "", vdmserr.Validation("", "unsupported lifecycle action")
}

func sortOrderFromProto(o vaultdmsv1.SortOrder) string {
	switch o {
	case vaultdmsv1.SortOrder_SORT_ORDER_ASC:
		return "asc"
	case vaultdmsv1.SortOrder_SORT_ORDER_DESC:
		return "desc"
	}
	return "desc"
}

// ---- Document -------------------------------------------------------------

func documentToProto(d *model.Document, p *service.DocumentPermissions) *vaultdmsv1.Document {
	if d == nil {
		return nil
	}
	meta, _ := mapToStruct(d.CustomMetadata)
	out := &vaultdmsv1.Document{
		Id:                       d.ID.String(),
		TenantId:                 d.TenantID.String(),
		WorkspaceId:              d.WorkspaceID.String(),
		FolderId:                 d.FolderID.String(),
		Title:                    d.Title,
		Description:              d.Description,
		LifecycleState:           lifecycleStateToProto(d.LifecycleState),
		RegionPin:                regionToProto(d.RegionPin),
		CustomMetadata:           meta,
		Tags:                     d.Tags,
		DocumentClass:            d.DocumentClass,
		ClassificationConfidence: float32(d.ClassificationConfidence),
		Sha256Hash:               d.SHA256Hash,
		TotalSizeBytes:           d.TotalSizeBytes,
		MimeType:                 d.MimeType,
		CreatedBy:                d.CreatedBy.String(),
		CreatedByName:            d.CreatedByName,
		CreatedAt:                timestamppb.New(d.CreatedAt),
		UpdatedAt:                timestamppb.New(d.UpdatedAt),
	}
	if d.CurrentVersionID != nil {
		out.CurrentVersionId = d.CurrentVersionID.String()
	}
	if d.WorkflowInstance != nil {
		out.WorkflowInstance = &vaultdmsv1.DocumentWorkflowInstance{
			Id:             d.WorkflowInstance.ID.String(),
			DefinitionId:   d.WorkflowInstance.DefinitionID.String(),
			DefinitionName: d.WorkflowInstance.DefinitionName,
			Status:         d.WorkflowInstance.Status,
			CurrentStep:    int32(d.WorkflowInstance.CurrentStep),
			StartedAt:      timestamppb.New(d.WorkflowInstance.StartedAt),
		}
	}
	if p != nil {
		out.Permissions = &vaultdmsv1.DocumentPermissions{
			CanView:   p.CanView,
			CanEdit:   p.CanEdit,
			CanDelete: p.CanDelete,
			CanShare:  p.CanShare,
			CanAdmin:  p.CanAdmin,
		}
	}
	return out
}

// ---- Folder ---------------------------------------------------------------

func folderToProto(f *model.Folder) *vaultdmsv1.Folder {
	if f == nil {
		return nil
	}
	out := &vaultdmsv1.Folder{
		Id:               f.ID.String(),
		TenantId:         f.TenantID.String(),
		WorkspaceId:      f.WorkspaceID.String(),
		Name:             f.Name,
		Path:             f.Path,
		Depth:            int32(f.Depth),
		DocumentCount:    f.DocumentCount,
		ChildFolderCount: f.ChildFolderCount,
		CreatedBy:        f.CreatedBy.String(),
		CreatedAt:        timestamppb.New(f.CreatedAt),
		UpdatedAt:        timestamppb.New(f.UpdatedAt),
	}
	if f.ParentFolderID != nil {
		out.ParentFolderId = f.ParentFolderID.String()
	}
	return out
}

// ---- Version --------------------------------------------------------------

func versionToProto(v *model.Version) *vaultdmsv1.Version {
	if v == nil {
		return nil
	}
	return &vaultdmsv1.Version{
		Id:            v.ID.String(),
		DocumentId:    v.DocumentID.String(),
		VersionNumber: int32(v.VersionNumber),
		ContentBlobId: v.ContentBlobID.String(),
		SizeBytes:     v.SizeBytes,
		MimeType:      v.MimeType,
		Sha256Hash:    v.SHA256Hash,
		CreatedBy:     v.CreatedBy.String(),
		CreatedByName: v.CreatedByName,
		CreatedAt:     timestamppb.New(v.CreatedAt),
		ChangeSummary: v.ChangeSummary,
	}
}

// ---- Share link -----------------------------------------------------------

func shareLinkToProto(l *model.ShareLink, publicBaseURL string) *vaultdmsv1.ShareLink {
	if l == nil {
		return nil
	}
	out := &vaultdmsv1.ShareLink{
		Id:                l.ID.String(),
		DocumentId:        l.DocumentID.String(),
		Token:             l.Token, // plaintext only on create — caller must not return hash
		Url:               publicBaseURL + "/shared/" + l.Token,
		PasswordProtected: l.PasswordHash != "",
		MaxViews:          int32(l.MaxViews),
		ViewCount:         int32(l.ViewCount),
		Permissions:       l.Permissions,
		IsActive:          l.IsActive,
		CreatedAt:         timestamppb.New(l.CreatedAt),
	}
	if l.ExpiresAt != nil {
		out.ExpiresAt = timestamppb.New(*l.ExpiresAt)
	}
	return out
}

// ---- Tag ------------------------------------------------------------------

func tagToProto(t *model.Tag) *vaultdmsv1.Tag {
	if t == nil {
		return nil
	}
	return &vaultdmsv1.Tag{
		Id:            t.ID.String(),
		Name:          t.Name,
		Color:         t.Color,
		DocumentCount: t.DocumentCount,
	}
}

// ---- json helpers ---------------------------------------------------------

// marshalMap is a convenience for producing []byte from a map when writing
// JSONB params; kept here to centralize error-free error-ignoring helpers.
func marshalMap(m map[string]any) []byte {
	if m == nil {
		return []byte("{}")
	}
	b, err := json.Marshal(m)
	if err != nil {
		return []byte("{}")
	}
	return b
}

var _ = marshalMap // keep exported-ish helper available to future handlers
