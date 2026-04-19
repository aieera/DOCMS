// Package handler translates the gRPC PolicyService surface to service
// calls. It maps proto → domain inputs, invokes the service, and maps
// domain errors → gRPC status codes.
package handler

import (
	"context"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/vaultdms/vaultdms/pkg/auth"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	vaultdmsv1 "github.com/vaultdms/vaultdms/proto/gen/go/vaultdms/v1"
	"github.com/vaultdms/vaultdms/services/policy/internal/service"
)

// Handler implements vaultdmsv1.PolicyServiceServer.
type Handler struct {
	vaultdmsv1.UnimplementedPolicyServiceServer
	svc *service.Service
}

// New constructs a handler.
func New(svc *service.Service) *Handler { return &Handler{svc: svc} }

// CheckPermission is the hot path. Tenant is pulled from the gRPC metadata
// by the TenantInterceptor in main.go; the proto carries only principal +
// resource + action + ABAC context.
func (h *Handler) CheckPermission(ctx context.Context, req *vaultdmsv1.CheckPermissionRequest) (*vaultdmsv1.CheckPermissionResponse, error) {
	tenantID, err := auth.GetTenantID(ctx)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "tenant required")
	}
	in := toServiceCheckInput(tenantID, req)
	res, err := h.svc.Check(ctx, in)
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	return &vaultdmsv1.CheckPermissionResponse{
		Allowed: res.Allowed,
		Reason:  res.Reason,
	}, nil
}

// BatchCheckPermission runs up to 50 checks in parallel.
func (h *Handler) BatchCheckPermission(ctx context.Context, req *vaultdmsv1.BatchCheckPermissionRequest) (*vaultdmsv1.BatchCheckPermissionResponse, error) {
	tenantID, err := auth.GetTenantID(ctx)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "tenant required")
	}
	inputs := make([]service.CheckInput, len(req.GetChecks()))
	for i, c := range req.GetChecks() {
		inputs[i] = toServiceCheckInput(tenantID, c)
	}
	results, err := h.svc.BatchCheck(ctx, inputs)
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	out := &vaultdmsv1.BatchCheckPermissionResponse{
		Results: make([]*vaultdmsv1.CheckPermissionResponse, len(results)),
	}
	for i, r := range results {
		out.Results[i] = &vaultdmsv1.CheckPermissionResponse{
			Allowed: r.Allowed,
			Reason:  r.Reason,
		}
	}
	return out, nil
}

// ---- proto → service mapping ---------------------------------------------

func toServiceCheckInput(tenantID uuid.UUID, req *vaultdmsv1.CheckPermissionRequest) service.CheckInput {
	return service.CheckInput{
		TenantID:     tenantID,
		SubjectType:  req.GetSubjectType(),
		SubjectID:    req.GetSubjectId(),
		Action:       req.GetAction(),
		ResourceType: req.GetResourceType(),
		ResourceID:   req.GetResourceId(),
		Context:      structToStringMap(req.GetContext()),
	}
}

// structToStringMap reduces a google.protobuf.Struct to map[string]string.
// ABAC attributes the policy uses are all scalars; non-string values are
// string-formatted via Go's default fmt.
func structToStringMap(s interface{ AsMap() map[string]any }) map[string]string {
	if s == nil {
		return map[string]string{}
	}
	src := s.AsMap()
	out := make(map[string]string, len(src))
	for k, v := range src {
		if v == nil {
			continue
		}
		switch vv := v.(type) {
		case string:
			out[k] = vv
		case bool:
			if vv {
				out[k] = "true"
			} else {
				out[k] = "false"
			}
		default:
			// Stringify numbers/arrays/etc. via json rendering would be
			// heavier; for the policy's purposes a %v is enough.
			out[k] = ""
		}
	}
	return out
}
