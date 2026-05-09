// gRPC-backed IngestSignedClient. Talks to:
//
//   - StorageService.InitiateUpload + CompleteUpload — receives a
//     presigned PUT URL, uploads the bytes, finalizes the upload.
//     The blob_id isn't returned by CompleteUpload (the proto stops
//     at storage_bucket/key) so we do a (tenant, sha256) lookup
//     against content_blobs; that's the same shortcut the document
//     service's storage_proxy.go uses.
//   - DocumentService.CreateVersion — mints the version row + emits
//     dms.version.uploaded.v1 (ADR 0021), which is what the
//     intelligence pipeline already subscribes to. We don't need a
//     bespoke "signed-pdf-arrived" event; the canonical version
//     event is sufficient.
//
// Tenant context propagation: the storage + document services
// require auth.GetTenantID(ctx) to resolve. We set X-Auth-Tenant-ID
// + X-User-ID on the outgoing gRPC metadata using
// pkg/auth.WithTenantID/WithUserID — the same helpers the upstream
// services use to populate ctx.
package service

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	vaultdmsv1 "github.com/vaultdms/vaultdms/proto/gen/go/vaultdms/v1"
	"google.golang.org/grpc/metadata"
)

// GRPCIngestClient is the production IngestSignedClient.
type GRPCIngestClient struct {
	storage  vaultdmsv1.StorageServiceClient
	document vaultdmsv1.DocumentServiceClient
	pool     *pgxpool.Pool
	http     *http.Client
}

// NewGRPCIngestClient wires the production hand-off. nil clients →
// any call returns "ingest not configured", which the service-layer
// caller treats as a soft failure.
func NewGRPCIngestClient(storage vaultdmsv1.StorageServiceClient, document vaultdmsv1.DocumentServiceClient, pool *pgxpool.Pool) *GRPCIngestClient {
	return &GRPCIngestClient{
		storage: storage, document: document, pool: pool,
		http: &http.Client{Timeout: 60 * time.Second},
	}
}

// PutSignedBlob: InitiateUpload → HTTP PUT → CompleteUpload → blob lookup.
func (c *GRPCIngestClient) PutSignedBlob(ctx context.Context, in PutSignedBlobInput) (*PutSignedBlobResult, error) {
	if c.storage == nil {
		return nil, errors.New("ingest: storage gRPC client not configured")
	}
	sha := hashBytes(in.Bytes)
	size := int64(len(in.Bytes))
	mdCtx := withTenantMetadata(ctx, in.TenantID, in.UserID)

	init, err := c.storage.InitiateUpload(mdCtx, &vaultdmsv1.InitiateUploadRequest{
		RegionPin:      in.RegionPin,
		Filename:       in.Filename,
		MimeType:       in.MimeType,
		SizeBytes:      size,
		ChecksumSha256: sha,
	})
	if err != nil {
		return nil, fmt.Errorf("storage initiate: %w", err)
	}

	// PUT bytes via the presigned URL. The required_headers map
	// from InitiateUpload passes through MD5 / cache-control if
	// the storage service ever sets them.
	headers := map[string]string{"Content-Type": in.MimeType}
	for k, v := range init.GetRequiredHeaders() {
		headers[k] = v
	}
	// Compute Content-MD5 if S3 needs it. Cheap; covers a class of
	// silent-corruption bugs.
	md5sum := md5.Sum(in.Bytes)
	headers["Content-MD5"] = hex.EncodeToString(md5sum[:])
	if err := httpPutWithRetry(ctx, c.http, init.GetPresignedPutUrl(), in.Bytes, headers); err != nil {
		return nil, fmt.Errorf("storage put: %w", err)
	}

	if _, err := c.storage.CompleteUpload(mdCtx, &vaultdmsv1.CompleteUploadRequest{
		UploadId:       init.GetUploadId(),
		ChecksumSha256: sha,
		SizeBytes:      size,
	}); err != nil {
		return nil, fmt.Errorf("storage complete: %w", err)
	}

	// Resolve content_blob_id by (tenant, sha256). Same lookup the
	// document service's storage_proxy.go does post-CompleteUpload
	// for the same reason — the proto doesn't surface blob_id.
	if c.pool == nil {
		return nil, errors.New("ingest: pool nil; cannot resolve blob id")
	}
	tenantUUID, err := uuid.Parse(in.TenantID)
	if err != nil {
		return nil, fmt.Errorf("tenant: %w", err)
	}
	var blobID string
	err = c.pool.QueryRow(ctx, `
		SELECT id::text FROM content_blobs
		 WHERE tenant_id = $1 AND sha256_hash = $2
		 ORDER BY created_at DESC LIMIT 1`,
		tenantUUID, sha).Scan(&blobID)
	if err != nil {
		return nil, fmt.Errorf("resolve blob id: %w", err)
	}
	return &PutSignedBlobResult{
		ContentBlobID: blobID, SHA256: sha, SizeBytes: size,
	}, nil
}

// CreateVersionFromBlob calls DocumentService.CreateVersion. The
// tenant + user metadata is propagated via gRPC headers so the
// receiving service's TenantInterceptor + UserIdentityInterceptor
// resolve them in the standard way.
func (c *GRPCIngestClient) CreateVersionFromBlob(ctx context.Context, in CreateVersionFromBlobInput) (string, error) {
	if c.document == nil {
		return "", errors.New("ingest: document gRPC client not configured")
	}
	mdCtx := withTenantMetadata(ctx, in.TenantID, in.UserID)
	v, err := c.document.CreateVersion(mdCtx, &vaultdmsv1.CreateVersionRequest{
		DocumentId:    in.DocumentID,
		ContentBlobId: in.ContentBlobID,
		ChangeSummary: in.ChangeSummary,
	})
	if err != nil {
		return "", fmt.Errorf("create version: %w", err)
	}
	return v.GetId(), nil
}

// withTenantMetadata sets the headers the gateway-internal
// services expect on every authenticated gRPC call. The two
// services we call (storage + document) share the same
// middleware stack so one set of headers covers both.
func withTenantMetadata(ctx context.Context, tenantID, userID string) context.Context {
	md := metadata.MD{}
	if tenantID != "" {
		md.Set("x-auth-tenant-id", tenantID)
	}
	if userID != "" {
		md.Set("x-user-id", userID)
	}
	return metadata.NewOutgoingContext(ctx, md)
}
