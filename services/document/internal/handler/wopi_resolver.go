// ADR 0065 — the concrete WOPIFileResolver.
//
// Maps a WOPI access token's claims (tenant, user, version_id=file_id,
// can_write) onto real rows and blobs:
//
//   - Resolve → CheckFileInfo metadata (doc title + size + write bit).
//   - Open    → streams the version's plaintext bytes (decrypting
//     envelope-encrypted blobs exactly like decrypt_stream.go).
//   - Save    → writes a NEW immutable version through the normal write
//     path: bytes go to the storage service (InitiateUpload →
//     direct S3 put → CompleteUpload: hash verify, MIME sniff, ClamAV,
//     envelope encryption, dedup) and the version row is appended via
//     DocumentService.CreateVersion (permission check, legal-hold /
//     record / WORM gates, head repoint, dms.version.uploaded.v1 +
//     notify outbox events). Nothing is ever overwritten in place.
//
// The same save core backs the OnlyOffice callback (SaveFromURL), so
// edit-in-Collabora and edit-in-OnlyOffice produce identical versions.
package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"google.golang.org/grpc/metadata"

	"github.com/aieera/sedoc/pkg/auth"
	pkgcrypto "github.com/aieera/sedoc/pkg/crypto"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/middleware"
	pkgstorage "github.com/aieera/sedoc/pkg/storage"
	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/service"
)

// wopiMaxSaveBytes caps an editor save-back. Office documents are far
// smaller; the cap only bounds worst-case memory (the storage complete
// path buffers the object anyway).
const wopiMaxSaveBytes = 256 << 20 // 256 MiB

// ErrWOPISaveAsUnsupported: PutRelativeFile (Save As) is not part of the
// edit-save DoD; return a clear error instead of a half-implemented copy.
var ErrWOPISaveAsUnsupported = errors.New("PutRelativeFile (Save As) is not supported; save creates a new version of the same document")

// DBWOPIResolver is the production WOPIFileResolver. All deps are
// constructor-injected; any nil dep fails the specific operation with a
// descriptive error rather than panicking (matches the handler's
// nil-resolver 503 posture).
type DBWOPIResolver struct {
	pool    *pgxpool.Pool
	s3      *pkgstorage.S3Client
	kms     pkgcrypto.KeyManager
	storage sedocv1.StorageServiceClient
	svc     *service.DocumentService
	log     zerolog.Logger
}

// NewDBWOPIResolver wires the resolver. s3+kms serve reads (decrypt);
// storage+svc serve writes (new version).
func NewDBWOPIResolver(
	pool *pgxpool.Pool,
	s3 *pkgstorage.S3Client,
	kms pkgcrypto.KeyManager,
	storageClient sedocv1.StorageServiceClient,
	svc *service.DocumentService,
	log zerolog.Logger,
) *DBWOPIResolver {
	return &DBWOPIResolver{pool: pool, s3: s3, kms: kms, storage: storageClient, svc: svc, log: log}
}

// identityCtx rebuilds the request-scoped caller identity from the WOPI
// claims. WOPI calls arrive from the editor with no session cookie, so
// the normal SessionAuth middleware never fires; the HMAC token is the
// auth boundary and its claims are the caller.
func (res *DBWOPIResolver) identityCtx(ctx context.Context, c *WOPIClaims) context.Context {
	return auth.WithUser(ctx, auth.UserInfo{ID: c.UserID, TenantID: c.TenantID})
}

// wopiVersionRow is the joined (document, version) view every operation
// needs.
type wopiVersionRow struct {
	DocID          uuid.UUID
	Title          string
	OwnerID        uuid.UUID
	LifecycleState string
	RegionPin      string
	VersionNumber  int
	SizeBytes      int64
	MimeType       string
	Bucket         string
	Key            string
	KEKID          string
	EncryptedDEK   []byte
	DEKNonce       []byte
}

func (res *DBWOPIResolver) loadVersion(ctx context.Context, tenantID, versionID uuid.UUID) (*wopiVersionRow, error) {
	var row wopiVersionRow
	err := database.WithTenantTx(ctx, res.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT d.id, d.title, d.created_by, d.lifecycle_state, d.region_pin,
			       v.version_number, v.size_bytes,
			       COALESCE(NULLIF(v.mime_type, ''), NULLIF(b.mime_type, ''), 'application/octet-stream'),
			       b.storage_bucket, b.storage_key,
			       COALESCE(b.kek_id, ''), b.encrypted_dek, b.dek_nonce
			FROM document_versions v
			JOIN documents d     ON d.tenant_id = v.tenant_id AND d.id = v.document_id
			JOIN content_blobs b ON b.tenant_id = v.tenant_id AND b.id = v.content_blob_id
			WHERE v.tenant_id = $1 AND v.id = $2
			  AND d.deleted_at IS NULL AND b.shredded_at IS NULL
		`, tenantID, versionID).Scan(
			&row.DocID, &row.Title, &row.OwnerID, &row.LifecycleState, &row.RegionPin,
			&row.VersionNumber, &row.SizeBytes, &row.MimeType,
			&row.Bucket, &row.Key, &row.KEKID, &row.EncryptedDEK, &row.DEKNonce,
		)
	})
	if err != nil {
		return nil, fmt.Errorf("version %s: %w", versionID, err)
	}
	return &row, nil
}

// Resolve backs CheckFileInfo.
func (res *DBWOPIResolver) Resolve(r *http.Request, claims *WOPIClaims) (*WOPIDoc, error) {
	ctx := res.identityCtx(r.Context(), claims)
	row, err := res.loadVersion(ctx, claims.TenantID, claims.FileID)
	if err != nil {
		return nil, err
	}
	// View permission — same fail-closed gate every download path uses.
	if res.svc != nil {
		if err := res.svc.EnsureCanViewDocument(ctx, row.DocID); err != nil {
			return nil, err
		}
	}
	// Write bit: the token's can_write AND the lifecycle allowing new
	// versions (legal hold / disposed states freeze edits). The policy
	// "edit" check re-runs authoritatively inside CreateVersion on save;
	// surfacing the lifecycle freeze here makes the editor open
	// read-only instead of failing at save time.
	canWrite := claims.CanWrite &&
		!model.IsLegalHoldBlocked(model.LifecycleState(row.LifecycleState), "create_version")
	return &WOPIDoc{
		BaseFileName:     wopiFileName(row.Title, row.MimeType),
		OwnerID:          row.OwnerID.String(),
		Size:             row.SizeBytes,
		Version:          fmt.Sprintf("%d", row.VersionNumber),
		UserCanWrite:     canWrite,
		UserCanRename:    false,
		UserFriendlyName: "User " + claims.UserID.String()[:8],
		DocumentID:       row.DocID.String(),
	}, nil
}

// Open backs GetFile: stream the version's plaintext. Envelope-encrypted
// blobs are unwrapped + decrypted in memory (same trade-off as
// decrypt_stream.go — GCM needs the full buffer to verify the tag).
func (res *DBWOPIResolver) Open(r *http.Request, claims *WOPIClaims) (io.ReadCloser, int64, error) {
	ctx := res.identityCtx(r.Context(), claims)
	row, err := res.loadVersion(ctx, claims.TenantID, claims.FileID)
	if err != nil {
		return nil, 0, err
	}
	if res.svc != nil {
		if err := res.svc.EnsureCanViewDocument(ctx, row.DocID); err != nil {
			return nil, 0, err
		}
	}
	if res.s3 == nil {
		return nil, 0, errors.New("wopi: s3 client not configured")
	}
	obj, err := res.s3.GetObject(ctx, row.Bucket, row.Key)
	if err != nil {
		return nil, 0, fmt.Errorf("s3 get %s/%s: %w", row.Bucket, row.Key, err)
	}
	if len(row.EncryptedDEK) == 0 {
		return obj, row.SizeBytes, nil
	}
	defer func() { _ = obj.Close() }()
	if res.kms == nil {
		return nil, 0, errors.New("wopi: kms not configured for encrypted blob")
	}
	plainDEK, err := res.kms.DecryptDataKey(ctx, row.KEKID, row.EncryptedDEK)
	if err != nil {
		return nil, 0, fmt.Errorf("dek unwrap: %w", err)
	}
	defer zero(plainDEK)
	ciphertext, err := io.ReadAll(obj)
	if err != nil {
		return nil, 0, fmt.Errorf("read ciphertext: %w", err)
	}
	plaintext, err := pkgcrypto.DecryptData(ciphertext, row.DEKNonce, plainDEK)
	if err != nil {
		return nil, 0, fmt.Errorf("gcm decrypt: %w", err)
	}
	return io.NopCloser(bytes.NewReader(plaintext)), int64(len(plaintext)), nil
}

// Save backs PutFile: the edited bytes become the document's next
// immutable version. The WOPI handler has already enforced the WOPI
// lock; CreateVersion enforces permission + lifecycle + records + WORM
// and emits the canonical events.
func (res *DBWOPIResolver) Save(r *http.Request, claims *WOPIClaims, body io.Reader) error {
	return res.saveVersion(r.Context(), claims, body, "Edited in office editor (WOPI)")
}

// SaveAs backs PutRelativeFile. Deliberately unsupported (see
// ErrWOPISaveAsUnsupported) — the editor surfaces the failure and the
// user keeps working on the same document.
func (res *DBWOPIResolver) SaveAs(_ *http.Request, _ *WOPIClaims, _ string, _ string, _ io.Reader) (string, string, string, error) {
	return "", "", "", ErrWOPISaveAsUnsupported
}

// SaveFromURL is the OnlyOffice save-back: the Document Server hands us
// a URL to the edited bytes; we download and commit through the same
// save core as WOPI PutFile. allowedHost guards SSRF — the URL must
// point at the configured Document Server (empty allowedHost = any
// http/https host; only used when SEDOC_ONLYOFFICE_URL is unset).
func (res *DBWOPIResolver) SaveFromURL(ctx context.Context, claims *WOPIClaims, fileURL, allowedHost, changeSummary string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return fmt.Errorf("bad download url: %w", err)
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return fmt.Errorf("download url scheme %q not allowed", req.URL.Scheme)
	}
	if allowedHost != "" && !strings.EqualFold(req.URL.Host, allowedHost) {
		return fmt.Errorf("download url host %q does not match the configured document server %q", req.URL.Host, allowedHost)
	}
	httpc := &http.Client{Timeout: 60 * time.Second}
	resp, err := httpc.Do(req)
	if err != nil {
		return fmt.Errorf("download edited file: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download edited file: status %d", resp.StatusCode)
	}
	return res.saveVersion(ctx, claims, resp.Body, changeSummary)
}

// saveVersion is the shared save core: buffer + hash the bytes, push
// them through the storage service upload pipeline (scan/encrypt/dedup),
// then append the version through DocumentService.CreateVersion so every
// gate + event of the normal write path fires.
func (res *DBWOPIResolver) saveVersion(parent context.Context, claims *WOPIClaims, body io.Reader, changeSummary string) error {
	if !claims.CanWrite {
		return errors.New("read-only token")
	}
	if res.storage == nil {
		return errors.New("wopi: storage service client not configured")
	}
	if res.svc == nil {
		return errors.New("wopi: document service not configured")
	}
	ctx := res.identityCtx(parent, claims)

	row, err := res.loadVersion(ctx, claims.TenantID, claims.FileID)
	if err != nil {
		return err
	}

	data, err := io.ReadAll(io.LimitReader(body, wopiMaxSaveBytes+1))
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	if int64(len(data)) > wopiMaxSaveBytes {
		return fmt.Errorf("save exceeds %d byte limit", int64(wopiMaxSaveBytes))
	}
	if len(data) == 0 {
		return errors.New("empty save body")
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])

	blobID, err := res.uploadBlob(ctx, claims, row, sha, data)
	if err != nil {
		return err
	}

	// The normal write path: permission ("edit" via policy), legal-hold /
	// record / WORM gates, next version number, head repoint,
	// dms.version.uploaded.v1 + notify outbox events — all CreateVersion.
	_, err = res.svc.CreateVersion(ctx, &service.CreateVersionInput{
		DocumentID:    row.DocID,
		ContentBlobID: blobID,
		SizeBytes:     int64(len(data)),
		MimeType:      row.MimeType,
		SHA256Hash:    sha,
		ChangeSummary: changeSummary,
	})
	if err != nil {
		return fmt.Errorf("create version: %w", err)
	}
	res.log.Info().
		Str("document_id", row.DocID.String()).
		Str("blob_id", blobID.String()).
		Str("summary", changeSummary).
		Msg("editor save-back created new version")
	return nil
}

// uploadBlob pushes the bytes through the storage service pipeline:
// InitiateUpload (permission + dedup) → direct S3 put at the returned
// coordinates → CompleteUpload (hash verify, MIME sniff, virus scan,
// envelope encryption, content_blobs row). On a dedup hit the existing
// blob is reused and no bytes move.
func (res *DBWOPIResolver) uploadBlob(ctx context.Context, claims *WOPIClaims, row *wopiVersionRow, sha string, data []byte) (uuid.UUID, error) {
	md := metadata.Pairs(
		middleware.TenantMetadataKey, claims.TenantID.String(),
		"x-user-id", claims.UserID.String(),
		// Scope the storage-side permission check to the document being
		// edited — same key the browser upload proxy forwards.
		"x-document-id", row.DocID.String(),
	)
	octx, cancel := context.WithTimeout(metadata.NewOutgoingContext(ctx, md), 60*time.Second)
	defer cancel()

	initResp, err := res.storage.InitiateUpload(octx, &sedocv1.InitiateUploadRequest{
		RegionPin:      row.RegionPin,
		Filename:       wopiFileName(row.Title, row.MimeType),
		MimeType:       row.MimeType,
		SizeBytes:      int64(len(data)),
		ChecksumSha256: sha,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("initiate upload: %w", err)
	}
	if initResp.GetDeduplicated() {
		id, perr := uuid.Parse(initResp.GetExistingBlobId())
		if perr != nil {
			return uuid.Nil, fmt.Errorf("dedup blob id: %w", perr)
		}
		return id, nil
	}

	// Server-side PUT straight at the storage coordinates. The presigned
	// URL in the response is signed for the browser-facing endpoint;
	// in-cluster we hold S3 credentials and write the same object the
	// URL points at, then CompleteUpload verifies + owns it.
	if res.s3 == nil {
		return uuid.Nil, errors.New("wopi: s3 client not configured for save")
	}
	if err := res.s3.PutObject(ctx, initResp.GetStorageBucket(), initResp.GetStorageKey(),
		bytes.NewReader(data), int64(len(data)), row.MimeType); err != nil {
		return uuid.Nil, fmt.Errorf("put object: %w", err)
	}
	if _, err := res.storage.CompleteUpload(octx, &sedocv1.CompleteUploadRequest{
		UploadId:       initResp.GetUploadId(),
		ChecksumSha256: sha,
		SizeBytes:      int64(len(data)),
	}); err != nil {
		return uuid.Nil, fmt.Errorf("complete upload: %w", err)
	}

	// CompleteUpload's proto doesn't return the blob id; resolve it by
	// (tenant, sha) exactly like the browser upload proxy does.
	var blobID uuid.UUID
	err = database.WithTenantTx(ctx, res.pool, claims.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT id FROM content_blobs
			WHERE tenant_id = $1 AND sha256_hash = $2
			ORDER BY created_at DESC LIMIT 1
		`, claims.TenantID, sha).Scan(&blobID)
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("resolve blob id by sha: %w", err)
	}
	return blobID, nil
}

// wopiFileName ensures the name the editor sees carries an extension —
// Collabora and OnlyOffice both key the editing app off it. Titles that
// already have one pass through unchanged.
func wopiFileName(title, mime string) string {
	if title == "" {
		title = "document"
	}
	// A 2–5 char suffix after a dot counts as an existing extension.
	if i := strings.LastIndex(title, "."); i > 0 {
		if n := len(title) - i - 1; n >= 2 && n <= 5 {
			return title
		}
	}
	if ext, ok := mimeExtensions[mime]; ok {
		return title + ext
	}
	return title
}

var mimeExtensions = map[string]string{
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   ".docx",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         ".xlsx",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": ".pptx",
	"application/msword":            ".doc",
	"application/vnd.ms-excel":      ".xls",
	"application/vnd.ms-powerpoint": ".ppt",
	"application/pdf":               ".pdf",
	"text/plain":                    ".txt",
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
