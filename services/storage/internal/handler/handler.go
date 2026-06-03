// Package handler implements vaultdmsv1.StorageServiceServer.
package handler

import (
	"context"

	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/google/uuid"

	"github.com/aieera/sedoc/pkg/auth"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	vaultdmsv1 "github.com/aieera/sedoc/proto/gen/go/vaultdms/v1"
	"github.com/aieera/sedoc/services/storage/internal/model"
	"github.com/aieera/sedoc/services/storage/internal/service"
)

// scopeFromMetadata pulls the upload-permission scope keys
// (x-document-id / x-folder-id / x-workspace-id) out of the inbound
// gRPC metadata. The upstream document service's storage proxy is
// responsible for forwarding the JSON-body scope into metadata; the
// keys are the standard ones documented in pkg/middleware. Returns
// nil for any key that's missing or not a valid UUID.
func scopeFromMetadata(ctx context.Context) (*uuid.UUID, *uuid.UUID, *uuid.UUID) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, nil, nil
	}
	parse := func(key string) *uuid.UUID {
		v := md.Get(key)
		if len(v) == 0 || v[0] == "" {
			return nil
		}
		id, err := uuid.Parse(v[0])
		if err != nil || id == uuid.Nil {
			return nil
		}
		return &id
	}
	return parse("x-document-id"), parse("x-folder-id"), parse("x-workspace-id")
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

	// Permission-scope fields come from gRPC metadata (the upstream
	// document service forwards them as x-document-id / x-folder-id /
	// x-workspace-id). The service layer's ensureUploadPermission
	// requires exactly one to know what to authorize against.
	docID, folderID, workspaceID := scopeFromMetadata(ctx)

	res, err := h.svc.InitiateUpload(ctx, service.InitiateUploadInput{
		TenantID:    tenantID,
		UserID:      userID,
		RegionPin:   req.GetRegionPin(),
		Filename:    req.GetFilename(),
		MimeType:    req.GetMimeType(),
		SizeBytes:   req.GetSizeBytes(),
		SHA256Hash:  req.GetChecksumSha256(),
		DocumentID:  docID,
		FolderID:    folderID,
		WorkspaceID: workspaceID,
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
