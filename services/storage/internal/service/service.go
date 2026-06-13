// Package service orchestrates object-storage operations across MinIO/S3,
// ClamAV, and Postgres. Flow for a single-PUT upload:
//
//  1. InitiateUpload → generate presigned PUT URL, persist upload_session
//  2. Client uploads directly to S3 using the presigned URL (bypasses us)
//  3. Client calls CompleteUpload → we fetch object metadata from S3
//     (size, etag), stream it back through ClamAV, record scan_result,
//     flip upload_session.status, emit dms.storage.upload_completed.v1
//  4. If the scan flags infected, we COPY the object to the quarantine
//     bucket, delete from the tier bucket, and set status='quarantined'
//  5. GetDownloadURL issues a short-lived presigned GET URL
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/aieera/sedoc/pkg/auth"
	pkgcrypto "github.com/aieera/sedoc/pkg/crypto"
	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/pkg/storage"
	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
	"github.com/aieera/sedoc/services/storage/internal/model"
	"github.com/aieera/sedoc/services/storage/internal/repository"
	"github.com/aieera/sedoc/services/storage/internal/scanner"
)

// PermissionChecker is the subset of policy's gRPC client used here.
// Declared locally so tests can inject a mock.
type PermissionChecker interface {
	CheckPermission(ctx context.Context, in *sedocv1.CheckPermissionRequest, opts ...grpc.CallOption) (*sedocv1.CheckPermissionResponse, error)
}

// Service is the storage-service facade. Handlers call into it.
type Service struct {
	pool    *pgxpool.Pool
	repos   *repository.Bundle
	s3      *storage.S3Client
	scanner *scanner.Client
	outbox  *database.OutboxRepository
	policy  PermissionChecker
	plans   *PlanLookup
	log     zerolog.Logger
	cfg     Config
	now     func() time.Time
}

// Config is DI + tunables. Handlers never see this.
type Config struct {
	Pool             *pgxpool.Pool
	Repos            *repository.Bundle
	S3               *storage.S3Client
	Scanner          *scanner.Client
	Outbox           *database.OutboxRepository
	Policy           PermissionChecker    // nil = permission check skipped (dev only; warn on startup)
	Plans            *PlanLookup          // nil = use MaxUploadSize flat; when set, MaxUploadSize acts as fallback
	KMS              pkgcrypto.KeyManager // required when EncryptAtRest=true
	Logger           zerolog.Logger
	DefaultRegion    string
	QuarantineBucket string
	UploadTTL        time.Duration
	MaxUploadSize    int64 // single-PUT ceiling; multipart lands in B1.1
	PresignTTL       time.Duration
	// PublicUploadBase is now applied at the S3 client layer (the
	// pkg/storage NewS3ClientWithPublicEndpoint constructor takes a
	// publicEndpoint and signs presigned URLs against it). Field kept
	// here for compose-level back-compat with deployments that read
	// it; service code no longer rewrites URLs post-sign because Sig V4
	// signatures are bound to the host header — rewriting after signing
	// produced SignatureDoesNotMatch on every upload.
	PublicUploadBase string
	// EncryptAtRest toggles per-file envelope encryption (AES-256-GCM with
	// KMS-wrapped DEK). Defaults to true when KMS is configured, false
	// otherwise. Bucket-level SSE-AES256 is always on regardless.
	EncryptAtRest bool
	// TenantKEKID is the default KEK identifier used when a per-tenant
	// lookup is not yet wired. Phase A2 follow-up: fetch per-tenant from
	// organizations.settings.
	TenantKEKID string
}

// New wires a Service. UploadTTL defaults 60 min, MaxUploadSize 5 GiB,
// PresignTTL 15 min.
func New(cfg Config) *Service {
	if cfg.UploadTTL <= 0 {
		cfg.UploadTTL = 60 * time.Minute
	}
	if cfg.MaxUploadSize <= 0 {
		cfg.MaxUploadSize = 5 * 1024 * 1024 * 1024
	}
	if cfg.PresignTTL <= 0 {
		cfg.PresignTTL = 15 * time.Minute
	}
	if cfg.DefaultRegion == "" {
		cfg.DefaultRegion = "us-east-1"
	}
	if cfg.QuarantineBucket == "" {
		cfg.QuarantineBucket = "dms-quarantine"
	}
	// TenantKEKID is retained only as a last-resort fallback when the
	// caller cannot derive a per-tenant alias (e.g. the health
	// endpoint that smoke-tests crypto without a real tenant in
	// context). In the live upload path, aliasForTenant(session.TenantID)
	// is used instead — see ADR 0022 and Wave 6 Prompt 6.1.
	if cfg.TenantKEKID == "" {
		cfg.TenantKEKID = "vaultdms/system/default"
	}
	// Default EncryptAtRest only when a KeyManager is actually wired.
	if cfg.KMS != nil && !cfg.EncryptAtRest {
		cfg.EncryptAtRest = true
	}
	return &Service{
		pool:    cfg.Pool,
		repos:   cfg.Repos,
		s3:      cfg.S3,
		scanner: cfg.Scanner,
		outbox:  cfg.Outbox,
		policy:  cfg.Policy,
		plans:   cfg.Plans,
		log:     cfg.Logger,
		cfg:     cfg,
		now:     time.Now,
	}
}

// ---- InitiateUpload -------------------------------------------------------

// InitiateUploadInput is the service-layer request shape.
type InitiateUploadInput struct {
	TenantID   uuid.UUID
	UserID     uuid.UUID
	RegionPin  string
	Filename   string
	MimeType   string
	SizeBytes  int64
	SHA256Hash string // optional; verified on CompleteUpload if non-empty

	// Permission-scope fields. Exactly one should be set — the handler
	// extracts it from the gRPC metadata (X-Document-ID, X-Folder-ID,
	// X-Workspace-ID). When all three are zero the permission check is
	// skipped and a warning is logged (dev/testing path).
	DocumentID  *uuid.UUID
	FolderID    *uuid.UUID
	WorkspaceID *uuid.UUID
}

// InitiateUploadResult is what handlers return to clients.
//
// When Deduplicated is true the bytes already exist in this region under
// ExistingBlobID; the server has already bumped the blob's reference
// count. The client must NOT upload — PresignedPutURL is empty. The
// caller (document service) should link the document to ExistingBlobID
// directly. Dedup requires the client to send SHA256Hash on initiate;
// without it the server takes the full upload path.
type InitiateUploadResult struct {
	UploadID        uuid.UUID
	PresignedPutURL string
	StorageBucket   string
	StorageKey      string
	ExpiresAt       time.Time
	Deduplicated    bool
	ExistingBlobID  *uuid.UUID
	SHA256Hash      string
}

// InitiateUpload generates a presigned PUT URL, persists the upload_session,
// and returns everything the client needs to push bytes to S3 directly.
func (s *Service) InitiateUpload(ctx context.Context, in InitiateUploadInput) (*InitiateUploadResult, error) {
	maxSize := s.cfg.MaxUploadSize
	if s.plans != nil && in.TenantID != uuid.Nil {
		maxSize = s.plans.MaxUploadSize(ctx, in.TenantID)
	}
	if err := validateInitiate(in, maxSize); err != nil {
		return nil, err
	}
	// MIME blocklist + extension blocklist at initiate time. This is the
	// cheap pre-check; magic-byte verification runs on complete when we
	// have the actual bytes.
	if scanner.IsBlockedMIME(in.MimeType) {
		return nil, vdmserr.Validation("mime_type", "executable mime types are not accepted")
	}
	if scanner.IsBlockedExtension(in.Filename) {
		return nil, vdmserr.Validation("filename", "executable file extensions are not accepted")
	}
	// Per-tenant allowlist (migration 000060). Empty allowlist = not
	// configured = no extra gate; the exec blocklist above still
	// applies. When configured, BOTH the MIME and the extension must
	// be in their respective allowlists. Best-effort: a DB error here
	// fails-open with a warning, so a Postgres outage can't strand
	// uploads.
	if err := s.enforceUploadPolicy(ctx, in.TenantID, in.MimeType, in.Filename); err != nil {
		return nil, err
	}
	// Permission check. The handler populates DocumentID / FolderID /
	// WorkspaceID from gRPC metadata headers (X-Document-ID etc.). At
	// least one must be present in production; dev paths may skip via
	// explicit config (Policy=nil). The preferred resource for the ACL
	// check is the most specific (document > folder > workspace).
	if err := s.ensureUploadPermission(ctx, in); err != nil {
		return nil, err
	}
	region := in.RegionPin
	if region == "" {
		region = s.cfg.DefaultRegion
	}

	// Deduplication — when the client declares the SHA-256 up front, look
	// for an existing content_blob with the same (tenant, region, sha256).
	// On hit: bump its reference count and return a result that tells the
	// client NOT to upload; the caller links the new document to the
	// existing blob. Permission has already been checked above, so this
	// does not leak existence to unauthorized callers.
	if in.SHA256Hash != "" {
		var existing *model.ContentBlob
		err := database.WithTenantTx(ctx, s.pool, in.TenantID, func(tx pgx.Tx) error {
			blob, err := s.repos.ContentBlobs.GetByHash(ctx, tx, in.TenantID, region, in.SHA256Hash)
			if err != nil {
				return err
			}
			if err := s.repos.ContentBlobs.IncrementRefCount(ctx, tx, in.TenantID, blob.ID); err != nil {
				return err
			}
			existing = blob
			return s.emit(ctx, tx, in.TenantID, blob.ID, "dms.storage.upload_deduplicated.v1", map[string]any{
				"content_blob_id": blob.ID.String(),
				"tenant_id":       in.TenantID.String(),
				"sha256_hash":     blob.SHA256Hash,
				"region":          region,
				"size_bytes":      blob.SizeBytes,
			})
		})
		if err == nil {
			blobID := existing.ID
			return &InitiateUploadResult{
				Deduplicated:   true,
				ExistingBlobID: &blobID,
				SHA256Hash:     existing.SHA256Hash,
				StorageBucket:  existing.StorageBucket,
				StorageKey:     existing.StorageKey,
			}, nil
		}
		// Miss (NotFound) → fall through to normal upload path. Other
		// errors are real and should surface.
		if vdmserr.KindOf(err) != vdmserr.KindNotFound {
			return nil, err
		}
	}

	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	bucket := bucketName(region, "hot")
	key := contentAddressableKey(in.TenantID, id, in.Filename)

	// pkg/storage already signs against the public endpoint when one
	// is configured (see NewS3ClientWithPublicEndpoint in main.go). No
	// post-sign rewrite — that path silently broke uploads via
	// SignatureDoesNotMatch.
	presignURL, err := s.s3.GeneratePresignedPutURL(ctx, bucket, key, s.cfg.UploadTTL)
	if err != nil {
		return nil, fmt.Errorf("presign put: %w", err)
	}

	now := s.now().UTC()
	expires := now.Add(s.cfg.UploadTTL)

	session := &model.UploadSession{
		TenantID:      in.TenantID,
		ID:            id,
		Filename:      sanitizeFilename(in.Filename),
		TotalSize:     in.SizeBytes,
		MimeType:      in.MimeType,
		UploadType:    model.UploadSingle,
		StorageRegion: region,
		Status:        model.UploadInitiated,
		CreatedBy:     in.UserID,
		CreatedAt:     now,
		ExpiresAt:     expires,
		StorageBucket: bucket,
		StorageKey:    key,
	}

	err = database.WithTenantTx(ctx, s.pool, in.TenantID, func(tx pgx.Tx) error {
		if err := s.repos.Uploads.Create(ctx, tx, session); err != nil {
			return err
		}
		return s.emit(ctx, tx, in.TenantID, id, "dms.storage.upload_initiated.v1", map[string]any{
			"upload_id":      id.String(),
			"tenant_id":      in.TenantID.String(),
			"storage_bucket": bucket,
			"storage_key":    key,
			"region":         region,
			"filename":       session.Filename,
			"size_bytes":     in.SizeBytes,
		})
	})
	if err != nil {
		return nil, err
	}
	return &InitiateUploadResult{
		UploadID:        id,
		PresignedPutURL: presignURL,
		StorageBucket:   bucket,
		StorageKey:      key,
		ExpiresAt:       expires,
	}, nil
}

// ---- CompleteUpload -------------------------------------------------------

// CompleteUploadInput is the client's "I finished PUTting" signal.
type CompleteUploadInput struct {
	TenantID   uuid.UUID
	UploadID   uuid.UUID
	SHA256Hash string // optional integrity check
	SizeBytes  int64  // claimed size; verified against S3 metadata
}

// CompleteUploadResult carries the scan outcome and final metadata.
type CompleteUploadResult struct {
	StorageBucket string
	StorageKey    string
	SizeBytes     int64
	SHA256Hash    string
	ScanResult    model.ScanResult
	Tier          string
}

// CompleteUpload is called by the client after the S3 PUT succeeds. We
// verify the object actually exists in S3, scan it synchronously, and
// move it to the quarantine bucket if ClamAV flags it.
//
// NOTE: synchronous scanning simplifies the flow but couples request
// latency to ClamAV. In B1.2 we'll move to async scanning with a pending
// state — this implementation is the correct observable behavior either way.
func (s *Service) CompleteUpload(ctx context.Context, in CompleteUploadInput) (*CompleteUploadResult, error) {
	if in.UploadID == uuid.Nil {
		return nil, vdmserr.Validation("upload_id", "required")
	}

	var session *model.UploadSession
	err := database.WithTenantTx(ctx, s.pool, in.TenantID, func(tx pgx.Tx) error {
		u, err := s.repos.Uploads.GetByID(ctx, tx, in.TenantID, in.UploadID)
		if err != nil {
			return err
		}
		if u.Status == model.UploadCompleted {
			session = u
			return vdmserr.ErrAlreadyExists
		}
		if u.Status == model.UploadQuarantine {
			return vdmserr.Conflict("upload quarantined")
		}
		if s.now().After(u.ExpiresAt) {
			_ = s.repos.Uploads.UpdateStatus(ctx, tx, in.TenantID, u.ID, model.UploadFailed)
			return vdmserr.Conflict("upload expired")
		}
		if err := s.repos.Uploads.UpdateStatus(ctx, tx, in.TenantID, u.ID, model.UploadScanning); err != nil {
			return err
		}
		session = u
		return nil
	})
	// Idempotent re-complete returns the existing result instead of erroring.
	if err != nil && !isIdempotentDupe(err) {
		return nil, err
	}
	if session == nil {
		return nil, vdmserr.ErrNotFound
	}

	// Recompute bucket/key — Create doesn't persist them (they're derived).
	bucket := bucketName(session.StorageRegion, "hot")
	key := contentAddressableKey(session.TenantID, session.ID, session.Filename)

	// 1. Sanity-check the object landed at the expected location + size.
	info, err := s.s3.GetObjectInfo(ctx, bucket, key)
	if err != nil {
		s.failUpload(ctx, session, "object not found in storage after complete")
		return nil, vdmserr.Conflict("object missing in storage")
	}
	if session.TotalSize > 0 && info.Size != session.TotalSize {
		s.failUpload(ctx, session, fmt.Sprintf("size mismatch: expected %d, got %d", session.TotalSize, info.Size))
		return nil, vdmserr.Conflict("size mismatch with S3 object")
	}

	// 2. Pull object bytes into memory once, then fan out: SHA-256 verify,
	//    MIME magic-byte detection, ClamAV scan, envelope encryption. The
	//    5 GiB single-PUT ceiling bounds worst-case memory use.
	objReader, err := s.s3.GetObject(ctx, bucket, key)
	if err != nil {
		s.failUpload(ctx, session, "fetch object for post-upload checks")
		return nil, vdmserr.Conflict("object fetch failed")
	}
	plainBuf, err := ensureBuffer(objReader)
	_ = objReader.Close()
	if err != nil {
		s.failUpload(ctx, session, "read object bytes")
		return nil, fmt.Errorf("read object: %w", err)
	}
	plainBytes := plainBuf.Bytes()

	// 2a. SHA-256 verification (optional — only if client declared a hash).
	sha := scanner.NewSHA256TeeReader(bytes.NewReader(plainBytes))
	_, _ = io.Copy(io.Discard, sha)
	actualSHA := sha.Sum()
	if in.SHA256Hash != "" && !scanner.VerifyHash(in.SHA256Hash, actualSHA) {
		s.failUpload(ctx, session, "sha256 mismatch")
		_ = s.s3.DeleteObject(ctx, bucket, key)
		return nil, vdmserr.Validation("sha256_hash",
			"client-declared hash does not match uploaded bytes")
	}

	// 2b. MIME magic-byte detection. If detected type is executable OR
	//     conflicts with declared type and detected is exec → quarantine.
	detected, _ := scanner.DetectFromBytes(plainBytes[:min(len(plainBytes), 262)])
	mimeMismatch := detected.IsKnown && !scanner.MIMEMatchesDeclared(session.MimeType, detected)
	detectedExec := detected.IsKnown && scanner.IsBlockedMIME(detected.MIME)
	mustQuarantineMIME := detectedExec || (mimeMismatch && detectedExec)

	// 3. ClamAV stream scan against the in-memory bytes (no second S3 GET).
	scanRes := s.scanBuffer(ctx, bytes.NewReader(plainBytes))
	if mustQuarantineMIME {
		scanRes.result = model.ScanInfected
		scanRes.signature = "BlockedMIME:" + detected.MIME
	}
	scanID, _ := uuid.NewV7()
	rec := &model.ScanRecord{
		TenantID:  session.TenantID,
		ID:        scanID,
		UploadID:  session.ID,
		Result:    scanRes.result,
		Signature: scanRes.signature,
		ScannedAt: s.now().UTC(),
	}

	// 4. If infected / MIME-blocked: COPY to quarantine, delete from hot.
	finalTier := "hot"
	if scanRes.result == model.ScanInfected {
		qKey := path.Join("infected", session.TenantID.String(), key)
		if err := s.s3.CopyObject(ctx, bucket, key, s.cfg.QuarantineBucket, qKey); err != nil {
			s.log.Error().Err(err).Str("upload_id", session.ID.String()).Msg("quarantine copy failed")
		} else {
			_ = s.s3.DeleteObject(ctx, bucket, key)
			bucket = s.cfg.QuarantineBucket
			key = qKey
		}
		finalTier = "quarantine"
	}

	// 5. Envelope encryption. If enabled, rewrap bytes with a fresh DEK and
	//    overwrite the S3 object with the ciphertext. The encrypted DEK +
	//    nonce are stored on the content_blobs row so decrypt-on-download
	//    has everything it needs. Skipped when the object is quarantined
	//    (re-uploading a malware ciphertext doesn't help anyone).
	var envResult *envelopeResult
	if s.cfg.EncryptAtRest && scanRes.result != model.ScanInfected {
		// ADR 0022: per-tenant KEK alias. liveKEKAlias resolves the
		// currently-live alias from tenant_keks (so a `kms rotate` takes
		// effect on new encrypts), falling back to the base
		// aliasForTenant form when the tenant has no tenant_keks row —
		// a no-op for every tenant that never ran `kms create`. The
		// LocalKeyManager HKDF-derives a unique 32-byte KEK per alias;
		// Vault / AWS KMS managers look the alias up in their native
		// stores.
		kekAlias := s.liveKEKAlias(ctx, session.TenantID)
		envResult, err = s.encryptAll(ctx, bytes.NewReader(plainBytes), kekAlias)
		if err != nil {
			s.failUpload(ctx, session, "envelope encrypt: "+err.Error())
			return nil, fmt.Errorf("encrypt: %w", err)
		}
		if err := s.s3.PutObject(ctx, bucket, key,
			bytes.NewReader(envResult.Ciphertext), int64(len(envResult.Ciphertext)),
			"application/octet-stream"); err != nil {
			s.failUpload(ctx, session, "re-upload encrypted bytes: "+err.Error())
			return nil, fmt.Errorf("re-upload: %w", err)
		}
	}

	// 6. Persist scan, content_blob, upload_session + emit event.
	var blobID uuid.UUID
	err = database.WithTenantTx(ctx, s.pool, in.TenantID, func(tx pgx.Tx) error {
		if err := s.repos.Scans.Record(ctx, tx, rec); err != nil {
			return err
		}
		if scanRes.result == model.ScanInfected {
			if err := s.repos.Uploads.UpdateStatus(ctx, tx, session.TenantID, session.ID, model.UploadQuarantine); err != nil {
				return err
			}
			return s.emit(ctx, tx, session.TenantID, session.ID, "dms.storage.upload_quarantined.v1", map[string]any{
				"upload_id":      session.ID.String(),
				"tenant_id":      session.TenantID.String(),
				"signature":      scanRes.signature,
				"storage_bucket": bucket,
				"storage_key":    key,
			})
		}
		// Happy path: insert content_blobs row capturing the final bytes,
		// the verified SHA-256 (not S3's ETag — ETag isn't SHA-256 for
		// multipart uploads), and the envelope encryption metadata.
		blobID, _ = uuid.NewV7()
		finalSize := int64(len(plainBytes))
		finalMime := session.MimeType
		if detected.IsKnown {
			finalMime = detected.MIME
		}
		blob := &model.ContentBlob{
			ID:             blobID,
			TenantID:       session.TenantID,
			SHA256Hash:     actualSHA,
			StorageRegion:  session.StorageRegion,
			StorageBucket:  bucket,
			StorageKey:     key,
			StorageClass:   finalTier,
			SizeBytes:      finalSize,
			MimeType:       finalMime,
			ReferenceCount: 1,
			CreatedAt:      s.now().UTC(),
		}
		if envResult != nil {
			blob.EncryptedDEK = envResult.EncryptedDEK
			blob.DEKNonce = envResult.Nonce
			blob.KEKID = envResult.KEKID
		}
		// Insert inside a SAVEPOINT (pgx tx.Begin creates a nested
		// pseudo-tx) so a duplicate-key violation doesn't abort the
		// outer transaction. Without the savepoint, the failing INSERT
		// leaves the outer tx in state 25P02 and every follow-up
		// query (GetByHash, IncrementRefCount, emit, …) errors out —
		// which is exactly the upload-complete-500 we hit in dev when
		// re-uploading a previously-seen file.
		insertErr := func() error {
			inner, err := tx.Begin(ctx)
			if err != nil {
				return err
			}
			if err := s.repos.ContentBlobs.Insert(ctx, inner, blob); err != nil {
				_ = inner.Rollback(ctx)
				return err
			}
			return inner.Commit(ctx)
		}()
		if insertErr != nil {
			// Late-detected dedup: same content already exists. Happens
			// when the client didn't compute SHA-256 up front (so the
			// initiate-time dedup check missed) and uploaded a file
			// with bytes that match an existing blob. Treat as a dedup
			// hit instead of failing — bump ref count, reuse the
			// existing blob, surface its ID via the same return path.
			if isIdempotentDupe(insertErr) {
				existing, lookupErr := s.repos.ContentBlobs.GetByHash(ctx, tx, session.TenantID, session.StorageRegion, actualSHA)
				if lookupErr != nil {
					return lookupErr
				}
				if incErr := s.repos.ContentBlobs.IncrementRefCount(ctx, tx, session.TenantID, existing.ID); incErr != nil {
					return incErr
				}
				// Best-effort cleanup of the just-uploaded duplicate
				// object so we don't leak storage. Failure here is
				// non-fatal; an orphan-blob sweeper handles drift.
				_ = s.s3.DeleteObject(ctx, bucket, key)
				blob = existing
				blobID = existing.ID
			} else {
				return insertErr
			}
		}
		if err := s.repos.Uploads.Complete(ctx, tx, session.TenantID, session.ID, s.now().UTC()); err != nil {
			return err
		}
		return s.emit(ctx, tx, session.TenantID, session.ID, "dms.storage.upload_completed.v1", map[string]any{
			"upload_id":       session.ID.String(),
			"content_blob_id": blobID.String(),
			"tenant_id":       session.TenantID.String(),
			"storage_bucket":  bucket,
			"storage_key":     key,
			"size_bytes":      finalSize,
			"sha256_hash":     actualSHA,
			"mime_type":       finalMime,
			"scan_result":     string(scanRes.result),
			"encrypted":       envResult != nil,
		})
	})
	if err != nil {
		return nil, err
	}

	return &CompleteUploadResult{
		StorageBucket: bucket,
		StorageKey:    key,
		SizeBytes:     int64(len(plainBytes)),
		SHA256Hash:    actualSHA,
		ScanResult:    scanRes.result,
		Tier:          finalTier,
	}, nil
}

// ---- Abort, Download, Scan status -----------------------------------------

// AbortUpload deletes the S3 object (if any) and marks the session failed.
func (s *Service) AbortUpload(ctx context.Context, tenantID, uploadID uuid.UUID) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		u, err := s.repos.Uploads.GetByID(ctx, tx, tenantID, uploadID)
		if err != nil {
			return err
		}
		if u.Status == model.UploadCompleted || u.Status == model.UploadQuarantine {
			return vdmserr.Conflict("upload already finalized")
		}
		bucket := bucketName(u.StorageRegion, "hot")
		key := contentAddressableKey(u.TenantID, u.ID, u.Filename)
		_ = s.s3.DeleteObject(ctx, bucket, key) // best effort
		return s.repos.Uploads.UpdateStatus(ctx, tx, tenantID, uploadID, model.UploadFailed)
	})
}

// GetDownloadURL returns a short-lived presigned GET URL for a completed
// upload. Rejects uploads that are quarantined or unscanned.
func (s *Service) GetDownloadURL(ctx context.Context, tenantID, uploadID uuid.UUID, ttlSeconds int) (string, time.Time, error) {
	var session *model.UploadSession
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		u, err := s.repos.Uploads.GetByID(ctx, tx, tenantID, uploadID)
		session = u
		return err
	})
	if err != nil {
		return "", time.Time{}, err
	}
	if session.Status != model.UploadCompleted {
		return "", time.Time{}, vdmserr.Conflict("upload not available for download")
	}
	ttl := time.Duration(ttlSeconds) * time.Second
	if ttl <= 0 || ttl > s.cfg.PresignTTL {
		ttl = s.cfg.PresignTTL
	}
	bucket := bucketName(session.StorageRegion, "hot")
	key := contentAddressableKey(session.TenantID, session.ID, session.Filename)
	// Force inline + the actual mime-type from the upload session so
	// Chrome doesn't fall back to its "what is this?" heuristic and
	// auto-download. The session row already carries the validated
	// MIME type from the upload-complete path.
	mime := session.MimeType
	if mime == "" {
		mime = "application/octet-stream"
	}
	url, err := s.s3.GeneratePresignedGetURLWith(ctx, bucket, key, ttl, "inline", session.Filename, mime)
	if err != nil {
		return "", time.Time{}, err
	}
	return url, s.now().Add(ttl), nil
}

// GetScanStatus returns the most recent scan outcome for an upload.
func (s *Service) GetScanStatus(ctx context.Context, tenantID, uploadID uuid.UUID) (*model.ScanRecord, error) {
	var rec *model.ScanRecord
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		r, err := s.repos.Scans.GetByUpload(ctx, tx, tenantID, uploadID)
		rec = r
		return err
	})
	return rec, err
}

// ---- internals ------------------------------------------------------------

type scanOutcome struct {
	result    model.ScanResult
	signature string
}

// scanObject streams the S3 object through ClamAV. An unreachable scanner
// returns ScanError; the caller decides whether to fail-closed (reject
// upload) or fail-open (warn + proceed). Current policy: log-and-accept
// with result=error, so the system keeps working during a ClamAV outage.
func (s *Service) scanObject(ctx context.Context, bucket, key string) scanOutcome {
	obj, err := s.s3.GetObject(ctx, bucket, key)
	if err != nil {
		return scanOutcome{result: model.ScanError}
	}
	defer obj.Close()
	return s.scanBuffer(ctx, obj)
}

// scanBuffer runs ClamAV against an already-fetched byte source. Used by
// CompleteUpload to avoid a second S3 GET when we already pulled bytes
// into memory for SHA-256 / MIME / envelope-encrypt.
func (s *Service) scanBuffer(ctx context.Context, r io.Reader) scanOutcome {
	if s.scanner == nil {
		return scanOutcome{result: model.ScanError}
	}
	res, err := s.scanner.Scan(ctx, r)
	if err != nil {
		s.log.Error().Err(err).Msg("clamav scan failed; fail-open")
		return scanOutcome{result: model.ScanError}
	}
	if res.Infected {
		return scanOutcome{result: model.ScanInfected, signature: res.Signature}
	}
	return scanOutcome{result: model.ScanClean}
}

// failUpload flips the session to failed + records a log entry. Best-effort;
// DB errors are logged and swallowed.
func (s *Service) failUpload(ctx context.Context, u *model.UploadSession, reason string) {
	s.log.Warn().Str("upload_id", u.ID.String()).Str("reason", reason).Msg("upload failed")
	_ = database.WithTenantTx(ctx, s.pool, u.TenantID, func(tx pgx.Tx) error {
		return s.repos.Uploads.UpdateStatus(ctx, tx, u.TenantID, u.ID, model.UploadFailed)
	})
}

func (s *Service) emit(ctx context.Context, tx pgx.Tx, tenantID, aggregateID uuid.UUID, eventType string, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	evt := database.NewOutboxEvent(tenantID, eventType, "upload", aggregateID, body)
	return s.outbox.Insert(ctx, tx, evt)
}

// ---- validation + helpers -------------------------------------------------

// enforceUploadPolicy applies the per-tenant allowlist from
// tenant_upload_policies. Empty lists (= unconfigured) short-circuit
// to nil so prior tenants keep working unchanged. When configured,
// BOTH the MIME and the extension must be in their respective lists
// — a partial match still rejects, which matches the admin's mental
// model ("only PDFs" should not let "evil.exe" through just because
// its sniffed MIME is application/pdf). DB read errors fail-OPEN with
// a warning: a brief Postgres blip must not strand the upload path
// for the whole org.
func (s *Service) enforceUploadPolicy(ctx context.Context, tenantID uuid.UUID, mime, filename string) error {
	if s.repos.UploadPolicies == nil {
		return nil
	}
	var pol *repository.UploadPolicy
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		p, gerr := s.repos.UploadPolicies.Get(ctx, tx, tenantID)
		if gerr != nil {
			return gerr
		}
		pol = p
		return nil
	})
	if err != nil {
		s.log.Warn().Err(err).Str("tenant_id", tenantID.String()).
			Msg("upload policy read failed; allowlist gate skipped")
		return nil
	}
	if pol == nil || (len(pol.AllowedMimeTypes) == 0 && len(pol.AllowedExtensions) == 0) {
		return nil
	}
	if len(pol.AllowedMimeTypes) > 0 {
		ok := false
		for _, m := range pol.AllowedMimeTypes {
			if strings.EqualFold(m, mime) {
				ok = true
				break
			}
		}
		if !ok {
			return vdmserr.Validation("mime_type", "this file type is not allowed by your organization")
		}
	}
	if len(pol.AllowedExtensions) > 0 {
		ext := strings.ToLower(path.Ext(filename))
		ok := false
		for _, e := range pol.AllowedExtensions {
			e = strings.ToLower(e)
			if !strings.HasPrefix(e, ".") {
				e = "." + e
			}
			if e == ext {
				ok = true
				break
			}
		}
		if !ok {
			return vdmserr.Validation("filename", "this file extension is not allowed by your organization")
		}
	}
	return nil
}

// ensureUploadPermission runs CheckPermission against the most-specific
// resource supplied in the input. Returns nil when the user is allowed,
// ErrForbidden on explicit deny, and a policy-service error on transport
// failure (fail closed — never allow on policy outage).
func (s *Service) ensureUploadPermission(ctx context.Context, in InitiateUploadInput) error {
	if s.policy == nil {
		s.log.Warn().Str("tenant_id", in.TenantID.String()).
			Msg("policy client not configured; permission check skipped (dev only)")
		return nil
	}
	resKind, resID := "", ""
	extra := map[string]any{}
	switch {
	case in.DocumentID != nil:
		resKind, resID = "document", in.DocumentID.String()
	case in.FolderID != nil:
		resKind, resID = "folder", in.FolderID.String()
		if in.WorkspaceID != nil {
			extra["workspace_id"] = in.WorkspaceID.String()
		}
	case in.WorkspaceID != nil:
		resKind, resID = "workspace", in.WorkspaceID.String()
	default:
		return vdmserr.Validation("resource_scope",
			"X-Document-ID, X-Folder-ID, or X-Workspace-ID header required for permission check")
	}
	// Forward role into the OPA context so Rule 6 (owner/admin) fires.
	if role := auth.GetUserRole(ctx); role != "" {
		extra["user_role"] = role
	}
	ctxStruct, err := structpb.NewStruct(extra)
	if err != nil {
		return fmt.Errorf("policy ctx build: %w", err)
	}
	// Append outgoing gRPC metadata so the policy service's
	// TenantInterceptor + UserIdentityInterceptor see the caller.
	// Without this every CheckPermission returned Unauthenticated and
	// the fail-closed branch below turned that into a 403.
	pairs := []string{
		"x-tenant-id", in.TenantID.String(),
		"x-user-id", in.UserID.String(),
	}
	if name := auth.GetUserName(ctx); name != "" {
		pairs = append(pairs, "x-user-name", name)
	}
	if role := auth.GetUserRole(ctx); role != "" {
		pairs = append(pairs, "x-user-role", role)
	}
	ctx = metadata.AppendToOutgoingContext(ctx, pairs...)
	resp, err := s.policy.CheckPermission(ctx, &sedocv1.CheckPermissionRequest{
		SubjectType:  "user",
		SubjectId:    in.UserID.String(),
		Action:       "edit",
		ResourceType: resKind,
		ResourceId:   resID,
		Context:      ctxStruct,
	})
	if err != nil {
		s.log.Error().Err(err).Msg("policy service unavailable; denying upload (fail-closed)")
		return vdmserr.ErrForbidden
	}
	if !resp.GetAllowed() {
		return vdmserr.ErrForbidden
	}
	return nil
}

func validateInitiate(in InitiateUploadInput, maxSize int64) error {
	if in.TenantID == uuid.Nil {
		return vdmserr.Validation("tenant_id", "required")
	}
	if in.UserID == uuid.Nil {
		return vdmserr.Validation("user_id", "required")
	}
	if in.SizeBytes <= 0 {
		return vdmserr.Validation("size_bytes", "must be > 0")
	}
	if in.SizeBytes > maxSize {
		return vdmserr.Validation("size_bytes", fmt.Sprintf("exceeds single-PUT ceiling of %d bytes; use multipart", maxSize))
	}
	if strings.TrimSpace(in.Filename) == "" {
		return vdmserr.Validation("filename", "required")
	}
	return nil
}

// bucketName follows the convention dms-{region}-{tier}.
// Keep this tight — the RegionEnforcer middleware parses this pattern too.
func bucketName(region, tier string) string {
	return "dms-" + region + "-" + tier
}

// contentAddressableKey spreads objects across the bucket's key space to
// avoid hot-prefix bottlenecks on S3. Pattern:
//
//	{tenant}/{yyyy}/{mm}/{upload_id}/{sanitized_filename}
//
// tenant first so a partial bucket listing by a compromised tenant key is
// bounded to that tenant's data.
func contentAddressableKey(tenantID, uploadID uuid.UUID, filename string) string {
	now := time.Now().UTC()
	return fmt.Sprintf("%s/%04d/%02d/%s/%s",
		tenantID, now.Year(), int(now.Month()), uploadID, sanitizeFilename(filename))
}

// sanitizeFilename strips path traversal and control characters.
func sanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.TrimSpace(name)
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	if out == "" {
		return "file"
	}
	if len(out) > 255 {
		out = out[:255]
	}
	return out
}

// rewriteHost replaces the scheme+host of a presigned URL with the
// publicUploadBase override, preserving the path and query string. Used
// when the S3 endpoint is internal but clients reach it via a proxy.
// rewriteHost was the pre-fix approach — broken because Sig V4
// signatures bind to the Host header. Removed; pkg/storage signs
// against the public endpoint directly.

func isIdempotentDupe(err error) bool {
	return err != nil && vdmserr.KindOf(err) == vdmserr.KindAlreadyExists
}

// silence unused imports in minimal builds
var _ io.Reader
