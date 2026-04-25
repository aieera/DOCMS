// Package handler implements vaultdmsv1.StorageServiceServer.
package handler

import (
	"context"

	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/google/uuid"

	"github.com/vaultdms/vaultdms/pkg/auth"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	vaultdmsv1 "github.com/vaultdms/vaultdms/proto/gen/go/vaultdms/v1"
	"github.com/vaultdms/vaultdms/services/storage/internal/model"
	"github.com/vaultdms/vaultdms/services/storage/internal/service"
)

// uuidFromMD reads a single header off the incoming gRPC metadata and
// parses it as a UUID. Returns nil for missing/empty/unparseable values
// — the caller decides whether nil is a hard error (permission scope)
// or just an absent optional.
func uuidFromMD(ctx context.Context, key string) *uuid.UUID {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil
	}
	vals := md.Get(key)
	if len(vals) == 0 || vals[0] == "" {
		return nil
	}
	id, err := uuid.Parse(vals[0])
	if err != nil {
		return nil
	}
	return &id
}

// Handler is the gRPC boundary.
type Handler struct {
	vaultdmsv1.UnimplementedStorageServiceServer
	svc *service.Service
}

// New constructs a handler.
func New(svc *service.Service) *Handler { return &Handler{svc: svc} }

// InitiateUpload maps the proto to a service call.
func (h *Handler) InitiateUpload(ctx context.Context, req *vaultdmsv1.InitiateUploadRequest) (*vaultdmsv1.InitiateUploadResponse, error) {
	tenantID, err := auth.GetTenantID(ctx)
	if err != nil {
		return nil, vdmserr.ToGRPCError(vdmserr.ErrUnauthorized)
	}
	userID, _ := auth.GetUserID(ctx)

	// Permission scope — the OPA check inside the service requires one
	// of (DocumentID, FolderID, WorkspaceID). The document REST proxy
	// (services/document/internal/handler/storage_proxy.go) forwards
	// these as gRPC metadata after lifting them from the request body;
	// read them back into the input here.
	folderID := uuidFromMD(ctx, "x-folder-id")
	workspaceID := uuidFromMD(ctx, "x-workspace-id")
	documentID := uuidFromMD(ctx, "x-document-id")

	res, err := h.svc.InitiateUpload(ctx, service.InitiateUploadInput{
		TenantID:    tenantID,
		UserID:      userID,
		RegionPin:   req.GetRegionPin(),
		Filename:    req.GetFilename(),
		MimeType:    req.GetMimeType(),
		SizeBytes:   req.GetSizeBytes(),
		SHA256Hash:  req.GetChecksumSha256(),
		FolderID:    folderID,
		WorkspaceID: workspaceID,
		DocumentID:  documentID,
	})
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	return &vaultdmsv1.InitiateUploadResponse{
		UploadId:         res.UploadID.String(),
		PresignedPutUrl:  res.PresignedPutURL,
		StorageBucket:    res.StorageBucket,
		StorageKey:       res.StorageKey,
		ExpiresAt:        timestamppb.New(res.ExpiresAt),
		RequiredHeaders:  map[string]string{}, // nothing today; wire cache-control here later if needed
	}, nil
}

// CompleteUpload finalizes the upload (incl. ClamAV scan).
func (h *Handler) CompleteUpload(ctx context.Context, req *vaultdmsv1.CompleteUploadRequest) (*vaultdmsv1.CompleteUploadResponse, error) {
	tenantID, err := auth.GetTenantID(ctx)
	if err != nil {
		return nil, vdmserr.ToGRPCError(vdmserr.ErrUnauthorized)
	}
	id, err := uuid.Parse(req.GetUploadId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(vdmserr.Validation("upload_id", "not a uuid"))
	}
	res, err := h.svc.CompleteUpload(ctx, service.CompleteUploadInput{
		TenantID:   tenantID,
		UploadID:   id,
		SHA256Hash: req.GetChecksumSha256(),
		SizeBytes:  req.GetSizeBytes(),
	})
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	return &vaultdmsv1.CompleteUploadResponse{
		StorageBucket:  res.StorageBucket,
		StorageKey:     res.StorageKey,
		SizeBytes:      res.SizeBytes,
		ChecksumSha256: res.SHA256Hash,
		ScanResult:     protoScanResult(res.ScanResult),
		Tier:           protoTier(res.Tier),
	}, nil
}

// AbortUpload cancels an in-flight upload.
func (h *Handler) AbortUpload(ctx context.Context, req *vaultdmsv1.AbortUploadRequest) (*vaultdmsv1.AbortUploadResponse, error) {
	tenantID, err := auth.GetTenantID(ctx)
	if err != nil {
		return nil, vdmserr.ToGRPCError(vdmserr.ErrUnauthorized)
	}
	id, err := uuid.Parse(req.GetUploadId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(vdmserr.Validation("upload_id", "not a uuid"))
	}
	if err := h.svc.AbortUpload(ctx, tenantID, id); err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	return &vaultdmsv1.AbortUploadResponse{Aborted: true}, nil
}

// GetDownloadURL returns a presigned GET.
func (h *Handler) GetDownloadURL(ctx context.Context, req *vaultdmsv1.GetDownloadURLRequest) (*vaultdmsv1.GetDownloadURLResponse, error) {
	tenantID, err := auth.GetTenantID(ctx)
	if err != nil {
		return nil, vdmserr.ToGRPCError(vdmserr.ErrUnauthorized)
	}
	// Today we key by document_id → upload_id resolution is deferred until
	// the document service wires the version_id → upload_id mapping via
	// storage_key. For now we treat document_id as an upload_id to unblock
	// integration testing; production wires the lookup through the
	// document service's version repo.
	id, err := uuid.Parse(req.GetDocumentId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(vdmserr.Validation("document_id", "not a uuid"))
	}
	url, expiresAt, err := h.svc.GetDownloadURL(ctx, tenantID, id, int(req.GetExpirySecs()))
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	return &vaultdmsv1.GetDownloadURLResponse{
		Url:       url,
		ExpiresAt: timestamppb.New(expiresAt),
	}, nil
}

// GetScanStatus returns the most recent scan outcome.
func (h *Handler) GetScanStatus(ctx context.Context, req *vaultdmsv1.GetScanStatusRequest) (*vaultdmsv1.ScanStatus, error) {
	tenantID, err := auth.GetTenantID(ctx)
	if err != nil {
		return nil, vdmserr.ToGRPCError(vdmserr.ErrUnauthorized)
	}
	id, err := uuid.Parse(req.GetUploadId())
	if err != nil {
		return nil, vdmserr.ToGRPCError(vdmserr.Validation("upload_id", "not a uuid"))
	}
	rec, err := h.svc.GetScanStatus(ctx, tenantID, id)
	if err != nil {
		return nil, vdmserr.ToGRPCError(err)
	}
	return &vaultdmsv1.ScanStatus{
		UploadId:  rec.UploadID.String(),
		Result:    protoScanResult(rec.Result),
		Signature: rec.Signature,
		ScannedAt: timestamppb.New(rec.ScannedAt),
	}, nil
}

// ---- enum mappers --------------------------------------------------------

func protoScanResult(r model.ScanResult) vaultdmsv1.ScanResult {
	switch r {
	case model.ScanClean:
		return vaultdmsv1.ScanResult_SCAN_RESULT_CLEAN
	case model.ScanInfected:
		return vaultdmsv1.ScanResult_SCAN_RESULT_INFECTED
	case model.ScanPending:
		return vaultdmsv1.ScanResult_SCAN_RESULT_PENDING
	case model.ScanError:
		return vaultdmsv1.ScanResult_SCAN_RESULT_ERROR
	}
	return vaultdmsv1.ScanResult_SCAN_RESULT_UNSPECIFIED
}

func protoTier(t string) vaultdmsv1.StorageTier {
	switch t {
	case "hot":
		return vaultdmsv1.StorageTier_STORAGE_TIER_HOT
	case "warm":
		return vaultdmsv1.StorageTier_STORAGE_TIER_WARM
	case "cold":
		return vaultdmsv1.StorageTier_STORAGE_TIER_COLD
	case "quarantine":
		return vaultdmsv1.StorageTier_STORAGE_TIER_QUARANTINE
	}
	return vaultdmsv1.StorageTier_STORAGE_TIER_UNSPECIFIED
}
