// Package sedoc is the integration product's client for the SeDoc DMS HTTP API,
// built against INTEGRATION.md + proto/gen/openapi/sedoc.swagger.json. It holds
// the service API key (never exposed to a browser), stamps an Idempotency-Key on
// every write, drives the 3-step upload, and classifies SeDoc's documented error
// codes into retryable vs terminal so the worker's queue can back off or
// dead-letter correctly.
package sedoc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"
)

// Client talks to the SeDoc document gateway.
type Client struct {
	baseURL   string // document gateway, e.g. http://localhost:8081/api/v1
	searchURL string // search service, e.g. http://localhost:8086/api/v1 (BFF only)
	apiKey    string // vdms_... ; sent as Authorization: Bearer
	hc        *http.Client
	limiter   *Limiter // optional proactive write throttle (nil = unthrottled)
}

// New constructs a client. baseURL should include the /api/v1 suffix.
func New(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: trimSlash(baseURL),
		apiKey:  apiKey,
		hc:      &http.Client{Timeout: 60 * time.Second},
	}
}

// WithLimiter attaches a shared proactive rate limiter. Every SeDoc write
// (POST/PUT/PATCH/DELETE issued through doJSON — :upsert, /ingest, storage
// initiate/complete, folder create/rename, restore, review resolve) waits on it
// before going out, so the worker and backfill collectively stay under SeDoc's
// 600/min ceiling. Reads (GET) and the presigned S3 PUT are NOT paced (the PUT
// doesn't hit the SeDoc API gateway). Pass the SAME *Limiter to every client
// that should share the budget. nil leaves the client unthrottled (the BFF's
// interactive read path). Returns the client for chaining.
func (c *Client) WithLimiter(l *Limiter) *Client {
	c.limiter = l
	return c
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

// APIError captures a non-2xx SeDoc response and its retry disposition.
type APIError struct {
	StatusCode    int
	Type          string // machine code from the error envelope (e.g. VALIDATION)
	Message       string
	CorrelationID string
	RetryAfter    time.Duration // from the Retry-After header on 429
}

func (e *APIError) Error() string {
	return fmt.Sprintf("sedoc %d %s: %s (correlation_id=%s)", e.StatusCode, e.Type, e.Message, e.CorrelationID)
}

// Retryable reports whether the worker should back off and retry rather than
// dead-letter. Per INTEGRATION.md §2:
//   - retryable: 409 IDEMPOTENCY_IN_PROGRESS, 503, 429, network/5xx.
//   - terminal:  400 (incl IDEMPOTENCY_KEY_*), 403, 404, 422 IDEMPOTENCY_KEY_REUSED.
func (e *APIError) Retryable() bool {
	switch {
	case e.StatusCode == http.StatusConflict && e.Type == "IDEMPOTENCY_IN_PROGRESS":
		return true
	case e.StatusCode == http.StatusTooManyRequests:
		return true
	case e.StatusCode == http.StatusServiceUnavailable:
		return true
	case e.StatusCode >= 500:
		return true
	default:
		return false
	}
}

// IsRetryable classifies any error (network errors are retryable; APIError
// defers to its disposition).
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	if ae, ok := err.(*APIError); ok {
		return ae.Retryable()
	}
	return true // transport / timeout / dial errors — retry
}

// RetryAfter returns the server-advised backoff for a 429, or 0.
func RetryAfter(err error) time.Duration {
	if ae, ok := err.(*APIError); ok {
		return ae.RetryAfter
	}
	return 0
}

// CorrelationID returns SeDoc's correlation id from an error, for support
// traceability on the sync_log row. Empty for non-API errors.
func CorrelationID(err error) string {
	if ae, ok := err.(*APIError); ok {
		return ae.CorrelationID
	}
	return ""
}

// HTTPStatus returns the HTTP status code from a SeDoc APIError, or 0 for
// non-API (transport) errors. The worker uses it to count observed 429s.
func HTTPStatus(err error) int {
	if ae, ok := err.(*APIError); ok {
		return ae.StatusCode
	}
	return 0
}

// ---- low-level request helper ---------------------------------------------

// isWriteMethod reports whether a method mutates and therefore counts against
// SeDoc's write rate limit. GETs (reads) are exempt.
func isWriteMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// doJSON executes an authenticated JSON request. idemKey, when non-empty, is
// sent as Idempotency-Key. out (when non-nil) receives the decoded 2xx body.
func (c *Client) doJSON(ctx context.Context, method, path, idemKey string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal body: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	// Proactively pace writes so a burst/backfill can't exceed SeDoc's 600/min
	// limit. Reads pass through unthrottled. The reactive 429+Retry-After backoff
	// in the worker remains as a second layer.
	if c.limiter != nil && isWriteMethod(method) {
		if err := c.limiter.Wait(ctx); err != nil {
			return err
		}
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err // transport error → IsRetryable=true
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parseAPIError(resp, raw)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("decode %s %s response: %w", method, path, err)
		}
	}
	return nil
}

func parseAPIError(resp *http.Response, raw []byte) *APIError {
	ae := &APIError{StatusCode: resp.StatusCode}
	var env struct {
		Type          string `json:"type"`
		Message       string `json:"message"`
		CorrelationID string `json:"correlation_id"`
	}
	_ = json.Unmarshal(raw, &env)
	ae.Type, ae.Message, ae.CorrelationID = env.Type, env.Message, env.CorrelationID
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil {
			ae.RetryAfter = time.Duration(secs) * time.Second
		}
	}
	if ae.Message == "" {
		ae.Message = http.StatusText(resp.StatusCode)
	}
	return ae
}

// ---- Folders ---------------------------------------------------------------

type folderResp struct {
	ID string `json:"id"`
}

// EnsureFolder creates a folder under parentID (nil = workspace root) and
// returns its id. idemKey makes it replay-safe — re-running the SAME logical
// create (same key) replays the original folder instead of minting a duplicate,
// which is how the worker keeps a shared bucket folder single across customers.
func (c *Client) EnsureFolder(ctx context.Context, idemKey, workspaceID string, parentID *string, name string) (string, error) {
	body := map[string]any{"name": name}
	if parentID != nil && *parentID != "" {
		body["parent_folder_id"] = *parentID
	}
	var out folderResp
	if err := c.doJSON(ctx, http.MethodPost,
		"/workspaces/"+workspaceID+"/folders", idemKey, body, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// RenameFolder updates a folder's display name (link-stable).
func (c *Client) RenameFolder(ctx context.Context, idemKey, folderID, name string) error {
	return c.doJSON(ctx, http.MethodPatch, "/folders/"+folderID, idemKey,
		map[string]any{"name": name}, nil)
}

// ---- Upload (3-step) -------------------------------------------------------

// InitiateUploadResult mirrors the initiate response. When Deduplicated is true
// the bytes already exist and the PUT can be skipped.
type InitiateUploadResult struct {
	UploadID        string `json:"upload_id"`
	PresignedPutURL string `json:"presigned_put_url"`
	StorageBucket   string `json:"storage_bucket"`
	StorageKey      string `json:"storage_key"`
	// Deduplicated isn't a field in the proxy response; the worker infers dedup
	// from an empty presigned URL + a non-empty key (storage returns the existing
	// blob coordinates with no PUT URL).
}

// CompleteUploadResult mirrors the complete response.
type CompleteUploadResult struct {
	StorageBucket  string `json:"storage_bucket"`
	StorageKey     string `json:"storage_key"`
	SizeBytes      int64  `json:"size_bytes"`
	ChecksumSHA256 string `json:"checksum_sha256"`
	ContentBlobID  string `json:"content_blob_id"`
}

// UploadResult is the outcome of the orchestrated upload.
type UploadResult struct {
	ContentBlobID string
	SHA256        string
	SizeBytes     int64
}

// UploadBlob runs the 3-step upload for in-memory bytes (the worker/backfill
// path, where the ERP render endpoint already returned the whole document). It
// hashes the bytes and delegates to UploadStream — no temp-file spool needed.
func (c *Client) UploadBlob(ctx context.Context, idemBase, regionPin, filename, mime string, data []byte) (*UploadResult, error) {
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	return c.UploadStream(ctx, idemBase, regionPin, filename, mime, bytes.NewReader(data), int64(len(data)), sha)
}

// UploadStream runs initiate → PUT → complete for an arbitrarily large body
// WITHOUT buffering it in memory, and returns the content_blob_id to reference
// from :upsert / /ingest. It is the entry point for the BFF's interactive
// uploads (the multipart part is streamed straight to the presigned PUT).
//
//   - sha256hex + size are required for the zero-copy streaming path: the sha
//     lets initiate dedup, and the size sets the PUT Content-Length (S3 PUT needs
//     a fixed length, not chunked). When the caller can supply both (a browser
//     computing SubtleCrypto over the file), the body is streamed through with
//     flat memory.
//   - If sha256hex is empty (or size <= 0), it falls back to spooling the body to
//     a temp FILE (not RAM) to hash it and learn its length, then streams the
//     file to the PUT. Disk-backed, still flat memory.
//
// The PUT is skipped on a dedup hit (initiate returns no presigned URL); the body
// is drained either way. The streamed byte count is verified against the declared
// size. idemBase seeds distinct per-step Idempotency-Keys (initiate/complete are
// different paths and MUST NOT share one — that would 422 as KEY_REUSED).
func (c *Client) UploadStream(ctx context.Context, idemBase, regionPin, filename, mime string, body io.Reader, size int64, sha256hex string) (*UploadResult, error) {
	sha := sha256hex
	src := body
	if sha == "" || size <= 0 {
		// Fallback: spool to a temp file, hashing as we go; then stream from disk.
		tmp, n, h, err := spoolAndHash(body)
		if err != nil {
			return nil, fmt.Errorf("spool: %w", err)
		}
		defer func() { _ = tmp.Close(); _ = os.Remove(tmp.Name()) }()
		sha, size, src = h, n, tmp
	}

	var init InitiateUploadResult
	if err := c.doJSON(ctx, http.MethodPost, "/storage/uploads/initiate", idemBase+":initiate",
		map[string]any{
			"region_pin": regionPin, "filename": filename, "mime_type": mime,
			"size_bytes": size, "sha256_hash": sha,
		}, &init); err != nil {
		return nil, fmt.Errorf("initiate: %w", err)
	}

	if init.PresignedPutURL != "" {
		cr := &countingReader{r: src}
		if err := c.putStream(ctx, init.PresignedPutURL, mime, cr, size); err != nil {
			return nil, fmt.Errorf("put: %w", err)
		}
		// Verify the streamed byte count matches what we declared + hashed.
		if cr.n != size {
			return nil, fmt.Errorf("upload size mismatch: streamed %d, declared %d", cr.n, size)
		}
		if extra, _ := io.Copy(io.Discard, src); extra > 0 {
			return nil, fmt.Errorf("upload size mismatch: source has %d byte(s) beyond declared %d", extra, size)
		}
	} else {
		// Dedup hit — SeDoc already holds this blob. Skip the PUT but drain the
		// source so the multipart part / connection is fully consumed.
		_, _ = io.Copy(io.Discard, src)
	}

	var done CompleteUploadResult
	if err := c.doJSON(ctx, http.MethodPost, "/storage/uploads/"+init.UploadID+"/complete", idemBase+":complete",
		map[string]any{"sha256_hash": sha, "size_bytes": size}, &done); err != nil {
		return nil, fmt.Errorf("complete: %w", err)
	}
	return &UploadResult{ContentBlobID: done.ContentBlobID, SHA256: sha, SizeBytes: size}, nil
}

// putStream PUTs a body of exactly size bytes to a presigned S3/MinIO URL. It
// sets ContentLength so net/http sends a fixed-length (NOT chunked) request — S3
// requires this — and streams the reader rather than buffering it. No SeDoc auth
// header (the URL is pre-signed); a non-2xx is a storage error (retryable). This
// PUT goes to object storage, not the SeDoc API gateway, so it is intentionally
// NOT paced by the write limiter (it doesn't consume the 600/min budget).
func (c *Client) putStream(ctx context.Context, url, contentType string, body io.Reader, size int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, body)
	if err != nil {
		return err
	}
	req.ContentLength = size // fixed-length streaming; net/http errors if body is short
	req.Header.Set("Content-Type", contentType)
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{StatusCode: resp.StatusCode, Type: "STORAGE_PUT", Message: "presigned PUT failed"}
	}
	return nil
}

// countingReader tallies bytes read, so an upload can verify the streamed count.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// spoolAndHash streams r to a temp file while computing its sha256, returning the
// rewound file, its byte length, and the hex digest. Disk-backed so even a very
// large body without a client-supplied hash stays flat in memory.
func spoolAndHash(r io.Reader) (*os.File, int64, string, error) {
	f, err := os.CreateTemp("", "sedoc-upload-*")
	if err != nil {
		return nil, 0, "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), r)
	if err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, 0, "", err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, 0, "", err
	}
	return f, n, hex.EncodeToString(h.Sum(nil)), nil
}

// ---- Upsert / Ingest -------------------------------------------------------

// UpsertInput is the :upsert request (we know the document identity).
type UpsertInput struct {
	WorkspaceID   string
	ExternalID    string
	FolderID      string
	Title         string
	DocumentClass string
	BlobChecksum  string
	BlobRef       string
	Mime          string
	ChangeSummary string
}

// UpsertResult mirrors the :upsert response.
type UpsertResult struct {
	DocumentID           string `json:"document_id"`
	CurrentVersionID     string `json:"current_version_id"`
	CurrentVersionNumber int    `json:"current_version_number"`
	Created              bool   `json:"created"`
	VersionCreated       bool   `json:"version_created"`
}

// UpsertByExternalKey create-or-versions a document keyed on the ERP business id.
func (c *Client) UpsertByExternalKey(ctx context.Context, idemKey string, in UpsertInput) (*UpsertResult, error) {
	body := map[string]any{
		"external_id":    in.ExternalID,
		"folder_id":      in.FolderID,
		"title":          in.Title,
		"document_class": in.DocumentClass,
		"change_summary": in.ChangeSummary,
		"version": map[string]any{
			"blob_checksum": in.BlobChecksum,
			"blob_ref":      in.BlobRef,
			"mime":          in.Mime,
		},
	}
	var out UpsertResult
	if err := c.doJSON(ctx, http.MethodPost,
		"/workspaces/"+in.WorkspaceID+"/documents:upsert", idemKey, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// IngestInput is the /ingest request (let SeDoc OCR + route).
type IngestInput struct {
	WorkspaceID       string
	FolderID          string
	TargetCustomerRef string
	BlobChecksum      string
	ContentBlobID     string
	Mime              string
	DocumentClass     string
}

// IngestResult mirrors the /ingest response.
type IngestResult struct {
	IngestionItemID string `json:"ingestion_item_id"`
	Status          string `json:"status"`
}

// Ingest stages a blob for OCR-then-route.
func (c *Client) Ingest(ctx context.Context, idemKey string, in IngestInput) (*IngestResult, error) {
	body := map[string]any{
		"workspace_id":        in.WorkspaceID,
		"folder_id":           in.FolderID,
		"target_customer_ref": in.TargetCustomerRef,
		"blob_checksum":       in.BlobChecksum,
		"content_blob_id":     in.ContentBlobID,
		"mime":                in.Mime,
		"document_class":      in.DocumentClass,
	}
	var out IngestResult
	if err := c.doJSON(ctx, http.MethodPost, "/ingest", idemKey, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
