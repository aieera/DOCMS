package bulk

import (
	"context"
	"errors"
	"io"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/vaultdms/vaultdms/pkg/auth"
	vaultdmsv1 "github.com/vaultdms/vaultdms/proto/gen/go/vaultdms/v1"
)

// GRPCServer adapts Service to the BulkService gRPC interface.
type GRPCServer struct {
	vaultdmsv1.UnimplementedBulkServiceServer
	svc *Service
}

func NewGRPCServer(svc *Service) *GRPCServer { return &GRPCServer{svc: svc} }

// BulkImport receives request batches over the bidi stream, processes
// each one, and acks with per-item Results. Backpressure is the
// stream's own — we don't read the next request until we've sent
// the previous response.
func (g *GRPCServer) BulkImport(stream vaultdmsv1.BulkService_BulkImportServer) error {
	tenantID, err := callerTenantUUID(stream.Context())
	if err != nil {
		return status.Error(codes.Unauthenticated, err.Error())
	}
	for {
		req, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		resp, perr := g.svc.ProcessBatch(stream.Context(), tenantID, req)
		if perr != nil {
			return status.Error(codes.Internal, perr.Error())
		}
		if err := stream.Send(resp); err != nil {
			return err
		}
	}
}

// BulkExport server-streams pages of matching records. Stops when
// the underlying iterator finds no more rows.
func (g *GRPCServer) BulkExport(req *vaultdmsv1.BulkExportRequest, stream vaultdmsv1.BulkService_BulkExportServer) error {
	tenantID, err := callerTenantUUID(stream.Context())
	if err != nil {
		return status.Error(codes.Unauthenticated, err.Error())
	}
	wsID := uuid.Nil
	if w := req.GetWorkspaceId(); w != "" {
		wsID, _ = uuid.Parse(w)
	}
	opts := ExportOptions{
		Resource:    req.GetResource(),
		WorkspaceID: wsID,
		PageSize:    req.GetPageSize(),
	}
	if t := req.GetFrom(); t != nil && t.IsValid() {
		opts.From = t.AsTime()
	}
	if t := req.GetTo(); t != nil && t.IsValid() {
		opts.To = t.AsTime()
	}
	return g.svc.Export(stream.Context(), tenantID, opts, func(page *vaultdmsv1.BulkExportResponse) error {
		return stream.Send(page)
	})
}

func callerTenantUUID(ctx context.Context) (uuid.UUID, error) {
	u, err := auth.User(ctx)
	if err == nil && u.TenantID != uuid.Nil {
		return u.TenantID, nil
	}
	return auth.GetTenantID(ctx)
}
