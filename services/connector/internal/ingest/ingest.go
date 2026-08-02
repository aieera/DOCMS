// Package ingest is the connector's server-side document ingest path.
//
// It runs the same 5-step flow the browser does in web/src/hooks/useUpload.ts,
// but from inside the cluster:
//
//  1. CreateDocument         → documents row (document service, REST)
//  2. InitiateUpload         → presigned PUT URL (storage service, gRPC)
//  3. PUT bytes              → blob lands in MinIO
//  4. CompleteUpload         → scan + persist content_blob (storage, gRPC)
//  5. CreateVersion          → links blob → document, fires
//     dms.version.uploaded.v1 (which drives OCR +
//     embed + search indexing)
//
// Two cluster-only wrinkles the browser doesn't hit:
//
//   - Auth. The document service's REST surface (CreateDocument /
//     CreateVersion) is gated by SessionOrAPIKey — a gateway signature
//     alone is NOT an identity. So we run as the caller: their dms_session
//     token is forwarded as a cookie on the document REST calls. The
//     storage upload is driven over gRPC directly (its interceptors
//     authorize off request metadata: tenant + user + folder scope),
//     bypassing the proxy's SessionOrAPIKey gate. Net effect: a user can
//     only import into folders they're allowed to write to.
//
//   - Presigned host. Storage signs PUT URLs against SEDOC_S3_PUBLIC_BASE
//     (localhost:9000 in dev) so the browser can reach MinIO. An in-cluster
//     caller can't reach localhost:9000, and SigV4 signs the Host header so
//     we can't rewrite the URL. Instead a custom dialer connects to the
//     internal MinIO endpoint (minio:9000) while the request keeps the
//     signed Host — MinIO validates the signature against the header, not
//     the socket.
package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/middleware"
	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
)

// Client runs the server-side ingest flow. Construct once at startup and
// reuse; the gRPC conn + HTTP clients are goroutine-safe.
type Client struct {
	docBaseURL    string
	gatewaySecret string
	internalKey   string // SEDOC_INTERNAL_API_KEY — used when no session token
	storage       sedocv1.StorageServiceClient
	storageConn   *grpc.ClientConn
	pool          *pgxpool.Pool
	httpc         *http.Client // document REST
	putc          *http.Client // presigned PUT, with the internal-host dialer
	log           zerolog.Logger
}

// New builds the ingest client. storageAddr defaults to storage:9090.
// The MinIO dialer rewrite is derived from SEDOC_S3_PUBLIC_BASE (the host
// storage signs against) → SEDOC_S3_ENDPOINT (the host we can actually
// reach in-cluster).
func New(pool *pgxpool.Pool, log zerolog.Logger) (*Client, error) {
	storageAddr := os.Getenv("STORAGE_SERVICE_ADDR")
	if storageAddr == "" {
		storageAddr = "storage:9090"
	}
	conn, err := grpc.NewClient(storageAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("dial storage grpc %s: %w", storageAddr, err)
	}

	docBase := os.Getenv("SEDOC_DOCUMENT_HTTP_URL")
	if docBase == "" {
		docBase = "http://document:8080"
	}

	return &Client{
		docBaseURL:    strings.TrimRight(docBase, "/"),
		gatewaySecret: os.Getenv("SEDOC_GATEWAY_SECRET"),
		internalKey:   os.Getenv("SEDOC_INTERNAL_API_KEY"),
		storage:       sedocv1.NewStorageServiceClient(conn),
		storageConn:   conn,
		pool:          pool,
		httpc:         &http.Client{Timeout: 30 * time.Second},
		putc:          newPresignedPUTClient(),
		log:           log,
	}, nil
}

// Close releases the storage gRPC connection.
func (c *Client) Close() error {
	if c.storageConn != nil {
		return c.storageConn.Close()
	}
	return nil
}

// newPresignedPUTClient returns an HTTP client whose dialer swaps the
// public MinIO host (what storage signs into the presigned URL, e.g.
// localhost:9000) for the in-cluster endpoint (minio:9000). The Host
// header — and thus the SigV4 signature — is untouched.
func newPresignedPUTClient() *http.Client {
	publicHost := os.Getenv("SEDOC_S3_PUBLIC_BASE") // e.g. "localhost:9000"
	internal := os.Getenv("SEDOC_S3_ENDPOINT")      // e.g. "http://minio:9000"
	internalHost := internal
	if u, err := url.Parse(internal); err == nil && u.Host != "" {
		internalHost = u.Host
	}
	base := &net.Dialer{Timeout: 10 * time.Second}
	return &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				if publicHost != "" && internalHost != "" && addr == publicHost {
					addr = internalHost
				}
				return base.DialContext(ctx, network, addr)
			},
		},
	}
}

// IngestFile runs the full create→upload→version flow for one file and
// returns the new document id. The work runs AS the caller: actorID +
// authToken are the importing user's id + session token. actorID rides
// in storage's gRPC metadata for the upload permission check; authToken
// is forwarded as the dms_session cookie on the document REST calls,
// which are gated by SessionOrAPIKey (a gateway-signature alone won't
// authenticate). So a user can only import into folders they can write.
func (c *Client) IngestFile(
	ctx context.Context,
	tenantID, actorID, authToken, workspaceID, folderID string,
	filename, contentType string,
	data []byte,
	customMetadata map[string]any,
) (string, error) {
	if workspaceID == "" || folderID == "" {
		return "", fmt.Errorf("ingest: workspace_id + folder_id required")
	}
	sum := sha256.Sum256(data)
	checksum := hex.EncodeToString(sum[:])

	// 1. Create the document row.
	docID, err := c.createDocument(ctx, tenantID, actorID, authToken, workspaceID, folderID, filename, customMetadata)
	if err != nil {
		return "", fmt.Errorf("create document: %w", err)
	}

	// 2. Initiate the upload (storage gRPC). Scope + identity ride in
	//    metadata; storage's interceptors + ensureUploadPermission read them.
	mdCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs(
		middleware.TenantMetadataKey, tenantID,
		"x-user-id", actorID,
		"x-user-role", "admin",
		"x-workspace-id", workspaceID,
		"x-folder-id", folderID,
	))
	initResp, err := c.storage.InitiateUpload(mdCtx, &sedocv1.InitiateUploadRequest{
		Filename:       filename,
		MimeType:       contentType,
		SizeBytes:      int64(len(data)),
		ChecksumSha256: checksum,
	})
	if err != nil {
		c.rollback(ctx, tenantID, actorID, authToken, docID)
		return "", fmt.Errorf("initiate upload: %w", err)
	}

	// 3+4. PUT the bytes, then complete (scan + persist blob).
	//
	//  On a dedup hit the bytes already exist in this region: storage has
	//  bumped the existing blob's refcount, opened NO upload session, and
	//  returned an empty presigned URL — so both steps must be skipped, as
	//  storage.proto states ("no PUT/complete needed"). The old shape only
	//  skipped the PUT and still called CompleteUpload, whose upload_id was
	//  then the zero UUID → "upload_id: required", rolling the whole ingest
	//  back. Every re-ingest of content already held by the tenant failed
	//  this way: re-filing the same mail, an attachment a user had uploaded
	//  before, or any retry after a partial failure.
	if !initResp.GetDeduplicated() {
		if err := c.putBytes(ctx, initResp.GetPresignedPutUrl(), contentType, data); err != nil {
			c.rollback(ctx, tenantID, actorID, authToken, docID)
			return "", fmt.Errorf("put bytes: %w", err)
		}
		if _, err := c.storage.CompleteUpload(mdCtx, &sedocv1.CompleteUploadRequest{
			UploadId:       initResp.GetUploadId(),
			ChecksumSha256: checksum,
			SizeBytes:      int64(len(data)),
		}); err != nil {
			c.rollback(ctx, tenantID, actorID, authToken, docID)
			return "", fmt.Errorf("complete upload: %w", err)
		}
	}

	// 5. Resolve the blob id (CompleteUpload doesn't return it through the
	//    proto — same (tenant, sha256) lookup the document storage proxy does)
	//    and link it as the document's first version. The version write fires
	//    dms.version.uploaded.v1 → OCR + embed + index.
	//    On the dedup path this resolves to the blob storage just refcounted.
	blobID := initResp.GetExistingBlobId()
	if blobID == "" {
		var berr error
		blobID, berr = c.lookupBlobID(ctx, tenantID, checksum)
		if berr != nil {
			c.rollback(ctx, tenantID, actorID, authToken, docID)
			return "", fmt.Errorf("resolve blob id: %w", berr)
		}
	}
	if err := c.createVersion(ctx, tenantID, actorID, authToken, docID, blobID); err != nil {
		c.rollback(ctx, tenantID, actorID, authToken, docID)
		return "", fmt.Errorf("create version: %w", err)
	}
	return docID, nil
}

// lookupBlobID resolves the content_blob created by CompleteUpload. RLS
// scopes the read to the tenant; the newest row for the checksum wins.
func (c *Client) lookupBlobID(ctx context.Context, tenantID, checksum string) (string, error) {
	tid, err := uuid.Parse(tenantID)
	if err != nil {
		return "", err
	}
	var blobID string
	err = database.WithTenantTx(ctx, c.pool, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT id::text FROM content_blobs
			 WHERE tenant_id = $1 AND sha256_hash = $2
			 ORDER BY created_at DESC LIMIT 1`,
			tid, checksum).Scan(&blobID)
	})
	if err != nil {
		return "", err
	}
	if blobID == "" {
		return "", fmt.Errorf("no content_blob for checksum %s", checksum)
	}
	return blobID, nil
}

// ---- document REST calls -------------------------------------------------

func (c *Client) createDocument(ctx context.Context, tenantID, actorID, authToken, workspaceID, folderID, title string, meta map[string]any) (string, error) {
	if meta == nil {
		meta = map[string]any{}
	}
	body, _ := json.Marshal(map[string]any{
		"workspace_id":    workspaceID,
		"folder_id":       folderID,
		"title":           title,
		"custom_metadata": meta,
	})
	var out struct {
		ID string `json:"id"`
	}
	if err := c.doDocJSON(ctx, http.MethodPost, "/api/v1/documents", tenantID, actorID, authToken, body, &out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", fmt.Errorf("document create returned no id")
	}
	return out.ID, nil
}

func (c *Client) createVersion(ctx context.Context, tenantID, actorID, authToken, docID, blobID string) error {
	body, _ := json.Marshal(map[string]any{
		"content_blob_id": blobID,
		"change_summary":  "ingested by connector",
	})
	return c.doDocJSON(ctx, http.MethodPost,
		"/api/v1/documents/"+docID+"/versions", tenantID, actorID, authToken, body, nil)
}

// rollback best-effort deletes the document row when a later step fails,
// so a partial import doesn't leave a versionless orphan (invisible in the
// UI, never OCR'd). Mirrors useUpload.ts's BUG-C2 cleanup.
func (c *Client) rollback(ctx context.Context, tenantID, actorID, authToken, docID string) {
	if docID == "" {
		return
	}
	if err := c.doDocJSON(ctx, http.MethodDelete, "/api/v1/documents/"+docID, tenantID, actorID, authToken, nil, nil); err != nil {
		c.log.Warn().Err(err).Str("document_id", docID).Msg("ingest: rollback delete failed")
	}
}

// doDocJSON calls the document REST surface as the caller. The CRUD routes
// are gated by SessionOrAPIKey, so a gateway signature alone is not an
// identity. Two auth modes:
//   - authToken set  → forward it as the dms_session cookie (acts AS that
//     user; used by Drive import, which has the triggering user's session).
//   - authToken empty → internal-service auth: present SEDOC_INTERNAL_API_KEY
//   - the tenant/user the worker acts on behalf of in headers (used by the
//     email + intake workers, which have no session). The gateway strips
//     inbound X-Internal-Service-Key so only in-cluster callers can use this.
//
// RequireGatewaySignature still wraps the service, so the signature is always
// required too.
func (c *Client) doDocJSON(ctx context.Context, method, path, tenantID, actorID, authToken string, body []byte, out any) error {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.docBaseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gateway-Signature", c.gatewaySecret)
	if authToken != "" {
		req.AddCookie(&http.Cookie{Name: "dms_session", Value: authToken})
		// Belt-and-suspenders: some auth paths read the bearer form.
		req.Header.Set("Authorization", "Bearer "+authToken)
	} else {
		req.Header.Set("X-Internal-Service-Key", c.internalKey)
		req.Header.Set("X-Auth-Tenant-ID", tenantID)
		req.Header.Set("X-Tenant-ID", tenantID)
		if actorID != "" {
			req.Header.Set("X-User-ID", actorID)
			req.Header.Set("X-User-Role", "admin")
		}
	}
	resp, err := c.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("%s %s: %d: %s", method, path, resp.StatusCode, string(b))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// putBytes uploads to the presigned URL via the internal-host dialer.
func (c *Client) putBytes(ctx context.Context, presignedURL, contentType string, data []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, presignedURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	req.ContentLength = int64(len(data))
	resp, err := c.putc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("presigned PUT %d: %s", resp.StatusCode, string(b))
	}
	return nil
}
