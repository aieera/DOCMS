// Package handler: workspace gRPC handlers. Wire these onto the
// generated UnimplementedDocumentServiceServer; the grpc-gateway REST
// mappings in document.proto bind them to /api/v1/workspaces/*.
package handler

import (
	"context"

	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	vaultdmsv1 "github.com/vaultdms/vaultdms/proto/gen/go/vaultdms/v1"
	"github.com/vaultdms/vaultdms/services/document/internal/model"
	"github.com/vaultdms/vaultdms/services/document/internal/service"
)

func (h *Handler) CreateWorkspace(ctx context.Context, req *vaultdmsv1.CreateWorkspaceRequest) (*vaultdmsv1.Workspace, error) {
	w, err := h.svc.CreateWorkspace(ctx, &service.CreateWorkspaceInput{
		Name:        req.GetName(),
		Description: req.GetDescription(),
		RegionPin:   req.GetRegionPin(),
	})
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	return workspaceToProto(w), nil
}

func (h *Handler) GetWorkspace(ctx context.Context, req *vaultdmsv1.GetWorkspaceRequest) (*vaultdmsv1.Workspace, error) {
	id, err := parseUUID("workspace_id", req.GetWorkspaceId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	w, err := h.svc.GetWorkspace(ctx, id)
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	return workspaceToProto(w), nil
}

func (h *Handler) ListWorkspaces(ctx context.Context, _ *vaultdmsv1.ListWorkspacesRequest) (*vaultdmsv1.ListWorkspacesResponse, error) {
	ws, err := h.svc.ListWorkspaces(ctx)
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	out := &vaultdmsv1.ListWorkspacesResponse{Workspaces: make([]*vaultdmsv1.Workspace, 0, len(ws))}
	for i := range ws {
		out.Workspaces = append(out.Workspaces, workspaceToProto(&ws[i]))
	}
	return out, nil
}

func (h *Handler) UpdateWorkspace(ctx context.Context, req *vaultdmsv1.UpdateWorkspaceRequest) (*vaultdmsv1.Workspace, error) {
	id, err := parseUUID("workspace_id", req.GetWorkspaceId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	w, err := h.svc.UpdateWorkspace(ctx, &service.UpdateWorkspaceInput{
		ID:          id,
		Name:        req.GetName(),
		Description: req.GetDescription(),
	})
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	return workspaceToProto(w), nil
}

func (h *Handler) DeleteWorkspace(ctx context.Context, req *vaultdmsv1.DeleteWorkspaceRequest) (*emptypb.Empty, error) {
	id, err := parseUUID("workspace_id", req.GetWorkspaceId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	if err := h.svc.DeleteWorkspace(ctx, id); err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	return &emptypb.Empty{}, nil
}

func workspaceToProto(w *model.Workspace) *vaultdmsv1.Workspace {
	if w == nil {
		return nil
	}
	out := &vaultdmsv1.Workspace{
		Id:            w.ID.String(),
		TenantId:      w.TenantID.String(),
		Name:          w.Name,
		Description:   w.Description,
		RegionPin:     w.RegionPin,
		CreatedBy:     w.CreatedBy.String(),
		CreatedAt:     timestamppb.New(w.CreatedAt),
		UpdatedAt:     timestamppb.New(w.UpdatedAt),
		DocumentCount: w.DocumentCount,
		FolderCount:   w.FolderCount,
		MemberCount:   w.MemberCount,
	}
	// Settings is stored as JSON bytes; surface as Struct for protobuf
	// callers and omit if empty/invalid.
	if len(w.Settings) > 0 {
		s := &structpb.Struct{}
		if err := s.UnmarshalJSON(w.Settings); err == nil {
			out.Settings = s
		}
	}
	return out
}
