// Package handler: storage REST proxy.
//
// The storage service is gRPC-only. The browser talks HTTP, so the
// document service exposes a thin set of REST handlers that forward to
// the storage gRPC client. The session auth middleware upstream
// populates X-Tenant-ID / X-User-ID headers (via the standard axios +
// auth-store pattern); the proxy copies those onto outbound gRPC
// metadata so the storage service sees the same caller context.
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/vaultdms/vaultdms/pkg/auth"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/pkg/middleware"
	vaultdmsv1 "github.com/vaultdms/vaultdms/proto/gen/go/vaultdms/v1"
)

const (
	userIDHeader   = "X-User-ID"
	proxyRPCBudget = 30 * time.Second
)

// StorageProxy forwards upload/download REST requests to the storage
// service over gRPC.
type StorageProxy struct {
	client vaultdmsv1.StorageServiceClient
}

// NewStorageProxy returns a proxy bound to the given storage gRPC client.
func NewStorageProxy(client vaultdmsv1.StorageServiceClient) *StorageProxy {
	return &StorageProxy{client: client}
}

// Register mounts the proxy routes on the given ServeMux:
//
//	POST /api/v1/storage/uploads/initiate
//	POST /api/v1/storage/uploads/{upload_id}/complete
//	POST /api/v1/storage/uploads/{upload_id}/abort
//	GET  /api/v1/storage/downloads/{document_id}/{version_id}
func (p *StorageProxy) Register(mux *http.ServeMux) {
	guard := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if p.client == nil {
				writeProxyJSON(w, http.StatusServiceUnavailable, map[string]any{
					"type":    "Unavailable",
					"message": "storage service unreachable",
				})
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("POST /api/v1/storage/uploads/initiate", guard(p.initiate))
	mux.HandleFunc("POST /api/v1/storage/uploads/{upload_id}/complete", guard(p.complete))
	mux.HandleFunc("POST /api/v1/storage/uploads/{upload_id}/abort", guard(p.abort))
	mux.HandleFunc("GET /api/v1/storage/downloads/{document_id}/{version_id}", guard(p.download))
}

func (p *StorageProxy) initiate(w http.ResponseWriter, r *http.Request) {
	// Accepts the frontend's historical field name sha256_hash as well
	// as the proto-native checksum_sha256. workspace_id / folder_id are
	// accepted and ignored here — they belong to document creation, not
	// the storage upload session.
	var in struct {
		RegionPin      string `json:"region_pin"`
		Filename       string `json:"filename"`
		MimeType       string `json:"mime_type"`
		SizeBytes      int64  `json:"size_bytes"`
		SHA256Hash     string `json:"sha256_hash"`
		ChecksumSHA256 string `json:"checksum_sha256"`
		WorkspaceID    string `json:"workspace_id,omitempty"`
		FolderID       string `json:"folder_id,omitempty"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	ctx, cancel := p.outbound(r)
	defer cancel()
	checksum := in.ChecksumSHA256
	if checksum == "" {
		checksum = in.SHA256Hash
	}
	resp, err := p.client.InitiateUpload(ctx, &vaultdmsv1.InitiateUploadRequest{
		RegionPin:      in.RegionPin,
		Filename:       in.Filename,
		MimeType:       in.MimeType,
		SizeBytes:      in.SizeBytes,
		ChecksumSha256: checksum,
	})
	if err != nil {
		writeGRPCErr(w, r, err)
		return
	}
	writeProxyJSON(w, http.StatusOK, map[string]any{
		"upload_id":         resp.GetUploadId(),
		"presigned_put_url": resp.GetPresignedPutUrl(),
		"storage_bucket":    resp.GetStorageBucket(),
		"storage_key":       resp.GetStorageKey(),
		"expires_at":        formatTs(resp.GetExpiresAt()),
		"required_headers":  resp.GetRequiredHeaders(),
	})
}

func (p *StorageProxy) complete(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SHA256Hash     string `json:"sha256_hash"`
		ChecksumSHA256 string `json:"checksum_sha256"`
		SizeBytes      int64  `json:"size_bytes"`
	}
	// Complete may be called with no body (frontend useUpload.ts posts an
	// empty body); tolerate that rather than rejecting on parse error.
	if r.ContentLength > 0 {
		if !decodeJSON(w, r, &in) {
			return
		}
	}
	checksum := in.ChecksumSHA256
	if checksum == "" {
		checksum = in.SHA256Hash
	}
	ctx, cancel := p.outbound(r)
	defer cancel()
	resp, err := p.client.CompleteUpload(ctx, &vaultdmsv1.CompleteUploadRequest{
		UploadId:       r.PathValue("upload_id"),
		ChecksumSha256: checksum,
		SizeBytes:      in.SizeBytes,
	})
	if err != nil {
		writeGRPCErr(w, r, err)
		return
	}
	writeProxyJSON(w, http.StatusOK, map[string]any{
		"storage_bucket":  resp.GetStorageBucket(),
		"storage_key":     resp.GetStorageKey(),
		"size_bytes":      resp.GetSizeBytes(),
		"checksum_sha256": resp.GetChecksumSha256(),
	})
}

func (p *StorageProxy) abort(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := p.outbound(r)
	defer cancel()
	if _, err := p.client.AbortUpload(ctx, &vaultdmsv1.AbortUploadRequest{
		UploadId: r.PathValue("upload_id"),
	}); err != nil {
		writeGRPCErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (p *StorageProxy) download(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := p.outbound(r)
	defer cancel()
	resp, err := p.client.GetDownloadURL(ctx, &vaultdmsv1.GetDownloadURLRequest{
		DocumentId: r.PathValue("document_id"),
		VersionId:  r.PathValue("version_id"),
	})
	if err != nil {
		writeGRPCErr(w, r, err)
		return
	}
	writeProxyJSON(w, http.StatusOK, map[string]any{
		"url":        resp.GetUrl(),
		"expires_at": formatTs(resp.GetExpiresAt()),
	})
}

// outbound builds a gRPC-outgoing context carrying the tenant + user
// identity. The storage service's TenantInterceptor reads x-tenant-id.
func (p *StorageProxy) outbound(r *http.Request) (context.Context, context.CancelFunc) {
	tenantID := r.Header.Get(middleware.TenantHeader)
	userID := r.Header.Get(userIDHeader)
	md := metadata.Pairs(
		middleware.TenantMetadataKey, tenantID,
		"x-user-id", userID,
	)
	ctx, cancel := context.WithTimeout(r.Context(), proxyRPCBudget)
	return metadata.NewOutgoingContext(ctx, md), cancel
}

// --- helpers ---------------------------------------------------------------

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeProxyJSON(w, http.StatusBadRequest, map[string]any{
			"type":    "INVALID_ARGUMENT",
			"message": "invalid json body",
		})
		return false
	}
	return true
}

func writeProxyJSON(w http.ResponseWriter, s int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(s)
	_ = json.NewEncoder(w).Encode(v)
}

// writeGRPCErr translates a gRPC status error into a JSON HTTP response.
//
// Domain errors produced by pkg/errors encode a JSON envelope
// ({type, message, details}) inside the gRPC status message; we unpack
// that here so MIME_MISMATCH / PAYLOAD_TOO_LARGE / UNAVAILABLE etc.
// reach the browser with the right HTTP status code AND the structured
// body the uploader reads (declared_mime, detected_mime, …).
//
// Plain gRPC errors from outside the pkg/errors helpers fall through to
// the code-string mapping below.
func writeGRPCErr(w http.ResponseWriter, r *http.Request, err error) {
	st, _ := status.FromError(err)
	corr := auth.GetCorrelationID(r.Context())

	if wire, ok := vdmserr.DecodeWire(st.Message()); ok {
		writeProxyJSON(w, httpStatusForWireType(wire.Type, st.Code().String()), map[string]any{
			"type":           wire.Type,
			"message":        wire.Message,
			"field":          wire.Field,
			"details":        wire.Details,
			"correlation_id": corr,
		})
		return
	}

	writeProxyJSON(w, grpcToHTTP(st.Code().String()), map[string]any{
		"type":           st.Code().String(),
		"message":        st.Message(),
		"correlation_id": corr,
	})
}

// httpStatusForWireType maps the domain error Code to an HTTP status.
// Kept in sync with pkg/errors/ToHTTPError — duplicating the mapping
// here avoids a dependency on HTTPError's construction just for the code.
func httpStatusForWireType(wireType, grpcCode string) int {
	switch wireType {
	case "MIME_MISMATCH", "CONFLICT", "LEGAL_HOLD", "ALREADY_EXISTS":
		return http.StatusConflict
	case "PAYLOAD_TOO_LARGE":
		return http.StatusRequestEntityTooLarge
	case "UNAVAILABLE":
		return http.StatusServiceUnavailable
	case "INVALID_ARGUMENT", "REGION_VIOLATION":
		return http.StatusBadRequest
	case "NOT_FOUND":
		return http.StatusNotFound
	case "FORBIDDEN":
		return http.StatusForbidden
	case "UNAUTHORIZED":
		return http.StatusUnauthorized
	case "RATE_LIMITED":
		return http.StatusTooManyRequests
	}
	return grpcToHTTP(grpcCode)
}

func grpcToHTTP(code string) int {
	switch code {
	case "OK":
		return http.StatusOK
	case "InvalidArgument":
		return http.StatusBadRequest
	case "FailedPrecondition":
		// gRPC FailedPrecondition is the semantic analogue of HTTP 409.
		// Previously mapped to 400 which was wrong for Conflict-kind
		// errors; callers that need a 400 surface it via InvalidArgument.
		return http.StatusConflict
	case "NotFound":
		return http.StatusNotFound
	case "AlreadyExists":
		return http.StatusConflict
	case "PermissionDenied":
		return http.StatusForbidden
	case "Unauthenticated":
		return http.StatusUnauthorized
	case "ResourceExhausted":
		return http.StatusTooManyRequests
	case "DeadlineExceeded":
		return http.StatusGatewayTimeout
	case "Unavailable":
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

func formatTs(t *timestamppb.Timestamp) string {
	if t == nil {
		return ""
	}
	return t.AsTime().UTC().Format(time.RFC3339)
}
