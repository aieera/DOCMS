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
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aieera/sedoc/pkg/database"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/middleware"
	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
	"github.com/aieera/sedoc/services/document/internal/service"
)

const (
	userIDHeader   = "X-User-ID"
	proxyRPCBudget = 30 * time.Second
)

// StorageProxy forwards upload/download REST requests to the storage
// service over gRPC.
type StorageProxy struct {
	client sedocv1.StorageServiceClient
	// pool is used for the post-CompleteUpload content_blob_id lookup.
	// Storage's CompleteUpload response doesn't carry the blob_id (the
	// proto wasn't designed for it; events publish it). The frontend
	// needs blob_id to call CreateVersion, so the proxy looks it up by
	// (tenant_id, sha256_hash) here. Avoids a proto regen.
	pool *pgxpool.Pool
	// svc gates the download path with EnsureCanViewDocument (FIX-2).
	// Optional — nil disables the check (used by older tests). Prod
	// wiring in cmd/server/main.go must always pass a non-nil svc.
	svc *service.DocumentService
}

// NewStorageProxy returns a proxy bound to the given storage gRPC
// client. pool may be nil — if so, the complete handler omits
// content_blob_id from the response. svc may be nil for tests; the
// production wiring in cmd/server/main.go always passes a non-nil
// service so the per-document view gate fires.
func NewStorageProxy(client sedocv1.StorageServiceClient, pool *pgxpool.Pool, svc *service.DocumentService) *StorageProxy {
	return &StorageProxy{client: client, pool: pool, svc: svc}
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

// RegisterDownloadAlias attaches a single route — the document-style
// download URL the frontend viewers (ImageAnnotationLayer,
// VideoAnnotationLayer, CoauthorEditor) plus onlyoffice_handler.go and
// redaction_review_handler.go all generate. Same `download` handler,
// different path shape, mounted on the caller-supplied mux so it can
// live outside the /api/v1/storage/ prefix.
func (p *StorageProxy) RegisterDownloadAlias(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/documents/{document_id}/versions/{version_id}/download", func(w http.ResponseWriter, r *http.Request) {
		if p.client == nil {
			writeProxyJSON(w, http.StatusServiceUnavailable, map[string]any{
				"type":    "Unavailable",
				"message": "storage service unreachable",
			})
			return
		}
		p.download(w, r)
	})
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
	// Storage's ensureUploadPermission requires one of the resource
	// scope keys in gRPC metadata: X-Document-ID / X-Folder-ID /
	// X-Workspace-ID. The frontend posts them as JSON body fields
	// (workspace_id / folder_id), so we forward into metadata here.
	scopePairs := []string{}
	if in.WorkspaceID != "" {
		scopePairs = append(scopePairs, "x-workspace-id", in.WorkspaceID)
	}
	if in.FolderID != "" {
		scopePairs = append(scopePairs, "x-folder-id", in.FolderID)
	}
	if len(scopePairs) > 0 {
		ctx = metadata.AppendToOutgoingContext(ctx, scopePairs...)
	}
	checksum := in.ChecksumSHA256
	if checksum == "" {
		checksum = in.SHA256Hash
	}
	resp, err := p.client.InitiateUpload(ctx, &sedocv1.InitiateUploadRequest{
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
	resp, err := p.client.CompleteUpload(ctx, &sedocv1.CompleteUploadRequest{
		UploadId:       r.PathValue("upload_id"),
		ChecksumSha256: checksum,
		SizeBytes:      in.SizeBytes,
	})
	if err != nil {
		writeGRPCErr(w, r, err)
		return
	}
	// Resolve the blob_id by sha256 + tenant. CompleteUpload doesn't
	// return it through the proto; the frontend needs it to call
	// CreateVersion (link blob → document).
	var blobID string
	if p.pool != nil {
		if tid, terr := auth.GetTenantID(r.Context()); terr == nil && tid != uuid.Nil {
			row := p.pool.QueryRow(r.Context(),
				`SELECT id::text FROM content_blobs
				 WHERE tenant_id = $1 AND sha256_hash = $2
				 ORDER BY created_at DESC LIMIT 1`,
				tid, resp.GetChecksumSha256())
			_ = row.Scan(&blobID)
		}
	}
	writeProxyJSON(w, http.StatusOK, map[string]any{
		"storage_bucket":   resp.GetStorageBucket(),
		"storage_key":      resp.GetStorageKey(),
		"size_bytes":       resp.GetSizeBytes(),
		"checksum_sha256":  resp.GetChecksumSha256(),
		"content_blob_id":  blobID,
	})
}

func (p *StorageProxy) abort(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := p.outbound(r)
	defer cancel()
	if _, err := p.client.AbortUpload(ctx, &sedocv1.AbortUploadRequest{
		UploadId: r.PathValue("upload_id"),
	}); err != nil {
		writeGRPCErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// download presigns a GET URL for a specific version's bytes. The
// storage gRPC service keys downloads by upload_session_id, not by
// document_id/version_id, because upload sessions are content-addressed
// and the upload happens before the document row exists. We resolve
// version_id → upload_session_id by parsing it out of the
// content_blobs.storage_key (which has shape
// `{tenant}/{YYYY}/{MM}/{upload_session_id}/{filename}`). Done in-proxy
// to avoid a proto change on GetDownloadURLRequest.
func (p *StorageProxy) download(w http.ResponseWriter, r *http.Request) {
	tenantID, err := auth.GetTenantID(r.Context())
	if err != nil || tenantID == uuid.Nil {
		writeProxyJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	versionID, err := uuid.Parse(r.PathValue("version_id"))
	if err != nil {
		writeProxyJSON(w, http.StatusBadRequest, map[string]any{"error": "version_id not a uuid"})
		return
	}
	docID, err := uuid.Parse(r.PathValue("document_id"))
	if err != nil {
		writeProxyJSON(w, http.StatusBadRequest, map[string]any{"error": "document_id not a uuid"})
		return
	}
	// FIX-2 (2026-05-31): per-document view check. The previous
	// implementation authorized purely by (tenantID, versionID) — any
	// authenticated tenant member who learned a version UUID pulled
	// the bytes (or had the server unwrap the DEK on the attacker's
	// behalf, for envelope-encrypted blobs). EnsureCanViewDocument
	// fails-closed on policy unavailability.
	if p.svc != nil {
		if err := p.svc.EnsureCanViewDocument(r.Context(), docID); err != nil {
			writeErr(w, r, err)
			return
		}
	}
	if p.pool == nil {
		writeProxyJSON(w, http.StatusInternalServerError, map[string]any{"error": "proxy missing db pool"})
		return
	}
	var (
		storageKey   string
		encryptedDEK []byte
	)
	err = database.WithTenantTx(r.Context(), p.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `
			SELECT b.storage_key, b.encrypted_dek
			FROM document_versions v
			JOIN content_blobs b ON b.tenant_id = v.tenant_id AND b.id = v.content_blob_id
			WHERE v.tenant_id = $1 AND v.id = $2
		`, tenantID, versionID).Scan(&storageKey, &encryptedDEK)
	})
	if err != nil {
		writeProxyJSON(w, http.StatusNotFound, map[string]any{"error": "version or blob not found"})
		return
	}
	// Envelope-encrypted blobs can't be served via a presigned MinIO
	// URL (the browser would fetch ciphertext). Route the caller to
	// the decrypt-stream endpoint instead. URL is a real route on
	// this service — no expiry needed.
	if len(encryptedDEK) > 0 {
		writeProxyJSON(w, http.StatusOK, map[string]any{
			"url": fmt.Sprintf(
				"/api/v1/documents/%s/versions/%s/decrypt-stream",
				r.PathValue("document_id"), r.PathValue("version_id"),
			),
		})
		return
	}
	uploadID := extractUploadID(storageKey)
	if uploadID == "" {
		writeProxyJSON(w, http.StatusInternalServerError, map[string]any{"error": "could not parse upload_id from storage_key"})
		return
	}
	ctx, cancel := p.outbound(r)
	defer cancel()
	resp, err := p.client.GetDownloadURL(ctx, &sedocv1.GetDownloadURLRequest{
		DocumentId: uploadID,
		VersionId:  r.PathValue("version_id"),
	})
	if err != nil {
		writeGRPCErr(w, r, err)
		return
	}
	p.emitDownloadAudit(r, tenantID, versionID)
	writeProxyJSON(w, http.StatusOK, map[string]any{
		"url":        resp.GetUrl(),
		"expires_at": formatTs(resp.GetExpiresAt()),
	})
}

// emitDownloadAudit writes a dms.document.downloaded.v1 outbox event
// best-effort. The audit consumer ingests it and lands a row in
// audit_events with actor + IP populated by the outbox publisher's
// CloudEvents envelope (Phase 1). Errors are logged but never
// surfaced to the caller — failing the download just because we
// couldn't log it would be worse UX.
func (p *StorageProxy) emitDownloadAudit(r *http.Request, tenantID, versionID uuid.UUID) {
	if p.pool == nil {
		return
	}
	docID, err := uuid.Parse(r.PathValue("document_id"))
	if err != nil {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"tenant_id":   tenantID.String(),
		"document_id": docID.String(),
		"version_id":  versionID.String(),
		"at":          time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return
	}
	evt := database.NewOutboxEvent(tenantID, "dms.document.downloaded.v1", "document", docID, payload)
	repo := database.NewOutboxRepository()
	_ = database.WithTenantTx(r.Context(), p.pool, tenantID, func(tx pgx.Tx) error {
		return repo.Insert(r.Context(), tx, evt)
	})
}

// extractUploadID parses the 4th path segment from a content-addressed
// storage key like `{tenant}/{YYYY}/{MM}/{upload_id}/{filename}`. Returns
// "" if the shape doesn't match.
func extractUploadID(storageKey string) string {
	parts := strings.Split(storageKey, "/")
	if len(parts) < 5 {
		return ""
	}
	return parts[3]
}

// outbound builds a gRPC-outgoing context carrying the tenant + user
// identity. The storage service's TenantInterceptor reads x-tenant-id.
func (p *StorageProxy) outbound(r *http.Request) (context.Context, context.CancelFunc) {
	// FIX-1 (2026-05-31): identity comes EXCLUSIVELY from the
	// SessionAuth-populated ctx. The previous fallback read tenant/
	// user/role from inbound headers, which Kong never stripped —
	// that gave any caller free choice of identity on the upstream
	// gRPC call. SessionAuthOptional is now wrapped at rootMux so
	// authenticated requests always carry trusted auth.UserInfo.
	var tenantID, userID, role string
	if u, err := auth.User(r.Context()); err == nil {
		tenantID = u.TenantID.String()
		userID = u.ID.String()
		role = u.Role
	}
	pairs := []string{
		middleware.TenantMetadataKey, tenantID,
		"x-user-id", userID,
		"x-user-role", role,
	}
	if name := auth.GetUserName(r.Context()); name != "" {
		pairs = append(pairs, "x-user-name", name)
	}
	md := metadata.Pairs(pairs...)
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

func writeGRPCErr(w http.ResponseWriter, r *http.Request, err error) {
	st, _ := status.FromError(err)
	code := grpcToHTTP(st.Code().String())
	writeProxyJSON(w, code, map[string]any{
		"type":           st.Code().String(),
		"message":        st.Message(),
		"correlation_id": auth.GetCorrelationID(r.Context()),
	})
}

func grpcToHTTP(code string) int {
	switch code {
	case "OK":
		return http.StatusOK
	case "InvalidArgument", "FailedPrecondition":
		return http.StatusBadRequest
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
