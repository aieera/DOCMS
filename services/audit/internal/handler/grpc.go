package handler

import (
	"context"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aieera/sedoc/pkg/auth"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
	"github.com/aieera/sedoc/services/audit/internal/model"
	"github.com/aieera/sedoc/services/audit/internal/service"
)

// GRPCHandler implements sedocv1.AuditServiceServer.
//
// This server was previously never registered (main.go built a grpc.Server
// but called no Register*), so the graphql-gateway's per-document Activity
// feed (Audit.Query) always came back empty — the gRPC method was
// unimplemented. The audit data + the REST/query service already existed; this
// just exposes the existing Service.List over the gRPC contract the gateway
// already calls.
type GRPCHandler struct {
	sedocv1.UnimplementedAuditServiceServer
	svc *service.Service
}

// NewGRPCHandler wires the gRPC audit server around the existing service.
func NewGRPCHandler(svc *service.Service) *GRPCHandler { return &GRPCHandler{svc: svc} }

// Query returns audit events for the caller's tenant, filtered by resource.
// The tenant is taken from the gRPC TenantInterceptor (ctx), never the request,
// so a caller can never read another tenant's audit trail.
func (h *GRPCHandler) Query(ctx context.Context, req *sedocv1.QueryAuditRequest) (*sedocv1.QueryAuditResponse, error) {
	tid, err := auth.GetTenantID(ctx)
	if err != nil || tid == uuid.Nil {
		return nil, vdmserr.ToGRPCError(vdmserr.ErrUnauthorized)
	}
	pageSize := int(req.GetPage().GetPageSize())
	if pageSize <= 0 {
		pageSize = 50
	}
	events, next, err := h.svc.List(ctx, model.ListFilter{
		TenantID:     tid.String(),
		ResourceType: req.GetResourceKind(),
		ResourceID:   req.GetResourceId(),
		PageSize:     pageSize,
		PageToken:    req.GetPage().GetCursor(),
	})
	if err != nil {
		return nil, err
	}
	resp := &sedocv1.QueryAuditResponse{Page: &sedocv1.PageResponse{NextCursor: next}}
	for _, e := range events {
		resp.Events = append(resp.Events, &sedocv1.AuditEvent{
			Id:           e.ID,
			TenantId:     e.TenantID,
			ActorId:      e.Actor,
			Action:       e.Action,
			ResourceKind: e.ResourceType,
			ResourceId:   e.ResourceID,
			OccurredAt:   timestamppb.New(e.CreatedAt),
		})
	}
	return resp, nil
}
