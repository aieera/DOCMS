// Package ingest is the connector's server-side document ingest path.
//
// It runs the same 5-step flow the browser does in web/src/hooks/useUpload.ts,
// but from inside the cluster:
//
//	1. CreateDocument         → documents row (document service, REST)
//	2. InitiateUpload         → presigned PUT URL (storage service, gRPC)
//	3. PUT bytes              → blob lands in MinIO
//	4. CompleteUpload         → scan + persist content_blob (storage, gRPC)
//	5. CreateVersion          → links blob → document, fires
//	                            dms.version.uploaded.v1 (which drives OCR +
//	                            embed + search indexing)
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
	docID, err := c.createDocument(ctx, authToken, workspaceID, folderID, filename, customMetadata)
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
		c.rollback(ctx, authToken, docID)
		return "", fmt.Errorf("initiate upload: %w", err)
	}

	// 3. PUT the bytes (skip when storage deduplicated to an existing blob —
	//    an empty presigned URL signals the dedup hit).
	if initResp.GetPresignedPutUrl() != "" {
		if err := c.putBytes(ctx, initResp.GetPresignedPutUrl(), contentType, data); err != nil {
			c.rollback(ctx, authToken, docID)
			return "", fmt.Errorf("put bytes: %w", err)
		}
	}

	// 4. Complete (scan + persist blob).
	if _, err := c.storage.CompleteUpload(mdCtx, &sedocv1.CompleteUploadRequest{
		UploadId:       initResp.GetUploadId(),
		ChecksumSha256: checksum,
		SizeBytes:      int64(len(data)),
	}); err != nil {
		c.rollback(ctx, authToken, docID)
		return "", fmt.Errorf("complete upload: %w", err)
	}

	// 5. Resolve the blob id (CompleteUpload doesn't return it through the
	//    proto — same (tenant, sha256) lookup the document storage proxy does)
	//    and link it as the document's first version. The version write fires
	//    dms.version.uploaded.v1 → OCR + embed + index.
	blobID, err := c.lookupBlobID(ctx, tenantID, checksum)
	if err != nil {
		c.rollback(ctx, authToken, docID)
		return "", fmt.Errorf("resolve blob id: %w", err)
	}
	if err := c.createVersion(ctx, authToken, docID, blobID); err != nil {
		c.rollback(ctx, authToken, docID)
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

func (c *Client) createDocument(ctx context.Context, authToken, workspaceID, folderID, title string, meta map[string]any) (string, error) {
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
	if err := c.doDocJSON(ctx, http.MethodPost, "/api/v1/documents", authToken, body, &out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", fmt.Errorf("document create returned no id")
	}
	return out.ID, nil
}

func (c *Client) createVersion(ctx context.Context, authToken, docID, blobID string) error {
	body, _ := json.Marshal(map[string]any{
		"content_blob_id": blobID,
		"change_summary":  "imported from Google Drive",
	})
	return c.doDocJSON(ctx, http.MethodPost,
		"/api/v1/documents/"+docID+"/versions", authToken, body, nil)
}

// rollback best-effort deletes the document row when a later step fails,
// so a partial import doesn't leave a versionless orphan (invisible in the
// UI, never OCR'd). Mirrors useUpload.ts's BUG-C2 cleanup.
func (c *Client) rollback(ctx context.Context, authToken, docID string) {
	if docID == "" {
		return
	}
	if err := c.doDocJSON(ctx, http.MethodDelete, "/api/v1/documents/"+docID, authToken, nil, nil); err != nil {
		c.log.Warn().Err(err).Str("document_id", docID).Msg("drive import: rollback delete failed")
	}
}

// doDocJSON calls the document REST surface as the importing user. The
// CRUD routes are gated by SessionOrAPIKey, so we forward the caller's
// dms_session token (cookie) — a gateway signature alone is not an
// identity. RequireGatewaySignature still wraps the service, so the
// signature header is also required.
func (c *Client) doDocJSON(ctx context.Context, method, path, authToken string, body []byte, out any) error {
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
