// Decrypt-stream download handler.
//
// The storage service's GetDownloadURL returns a presigned MinIO URL
// pointing straight at the bucket. That works for unencrypted blobs
// but fails for envelope-encrypted blobs (encrypted_dek populated):
// the browser fetches raw AES-GCM ciphertext and react-pdf / image
// viewers can't parse it.
//
// This handler closes that gap. It owns the read path for encrypted
// blobs: load blob metadata → fetch ciphertext from S3 → unwrap the
// per-blob DEK via the tenant KEK → AES-GCM decrypt → stream
// plaintext with the original mime type.
//
// Unencrypted blobs work too (pass-through copy) so callers can hit
// the same URL regardless of encryption state — useful for the
// frontend, which doesn't know whether a given blob was encrypted.
//
// AES-GCM is read fully into memory before the auth tag can be
// verified; matches the encrypt path in services/storage. 5 GiB
// single-PUT ceiling applies here too.
package handler

import (
	"io"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/pkg/auth"
	pkgcrypto "github.com/vaultdms/vaultdms/pkg/crypto"
	"github.com/vaultdms/vaultdms/pkg/database"
	"github.com/vaultdms/vaultdms/pkg/storage"
	"github.com/vaultdms/vaultdms/services/document/internal/service"
)

// DecryptStreamHandler exposes a single GET endpoint that decrypts
// (when needed) and streams a version's blob. All deps are
// constructor-injected so the unit tests can swap in fakes.
type DecryptStreamHandler struct {
	pool *pgxpool.Pool
	s3   *storage.S3Client
	kms  pkgcrypto.KeyManager
	log  zerolog.Logger
	// svc gates the stream with EnsureCanViewDocument (FIX-2). Nil
	// disables the check (legacy tests). Prod wiring in main.go always
	// passes a non-nil service.
	svc *service.DocumentService
}

// NewDecryptStreamHandler wires deps. Either of `s3` or `kms` may be
// nil — encrypted-blob requests fail with 503 in that case; unencrypted
// requests still pass through if s3 is set. svc may be nil for tests;
// production wiring always passes a non-nil service so the per-
// document view gate fires.
func NewDecryptStreamHandler(
	pool *pgxpool.Pool,
	s3 *storage.S3Client,
	kms pkgcrypto.KeyManager,
	svc *service.DocumentService,
	log zerolog.Logger,
) *DecryptStreamHandler {
	return &DecryptStreamHandler{pool: pool, s3: s3, kms: kms, svc: svc, log: log}
}

// Register mounts:
//
//	GET /api/v1/documents/{document_id}/versions/{version_id}/decrypt-stream
func (h *DecryptStreamHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc(
		"GET /api/v1/documents/{document_id}/versions/{version_id}/decrypt-stream",
		h.serve,
	)
}

func (h *DecryptStreamHandler) serve(w http.ResponseWriter, r *http.Request) {
	tenantID, err := auth.GetTenantID(r.Context())
	if err != nil || tenantID == uuid.Nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	versionID, err := uuid.Parse(r.PathValue("version_id"))
	if err != nil {
		http.Error(w, "version_id not a uuid", http.StatusBadRequest)
		return
	}
	docID, err := uuid.Parse(r.PathValue("document_id"))
	if err != nil {
		http.Error(w, "document_id not a uuid", http.StatusBadRequest)
		return
	}
	// FIX-2 (2026-05-31): per-document view check. Previously the
	// stream loaded blob metadata + unwrapped the DEK for ANY known
	// (tenantID, versionID) — IDOR with envelope-encryption-as-a-
	// service for the attacker.
	if h.svc != nil {
		if err := h.svc.EnsureCanViewDocument(r.Context(), docID); err != nil {
			writeErr(w, r, err)
			return
		}
	}
	if h.s3 == nil {
		h.log.Error().Msg("decrypt-stream: s3 client not configured")
		http.Error(w, "storage not configured", http.StatusServiceUnavailable)
		return
	}

	// Load blob metadata for this version. RLS enforces tenant scope;
	// we still pass tenant_id explicitly so a mis-set GUC fails-closed.
	var (
		bucket, key, mimeType, kekID string
		encryptedDEK, dekNonce       []byte
	)
	// BUG-05: prefer the version's mime_type over the blob's. The
	// blob row's mime is set by the storage uploader from raw
	// Content-Type guesses (often application/octet-stream); the
	// version row's mime comes from the explicit document API and
	// is what every other surface displays. When react-pdf gets
	// application/octet-stream it refuses to parse, so PDFs that
	// happened to be uploaded with the wrong blob mime never
	// previewed.
	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `
			SELECT b.storage_bucket,
			       b.storage_key,
			       COALESCE(NULLIF(v.mime_type, ''), NULLIF(b.mime_type, ''), 'application/octet-stream'),
			       COALESCE(b.kek_id, ''),
			       b.encrypted_dek,
			       b.dek_nonce
			FROM document_versions v
			JOIN content_blobs b
			  ON b.tenant_id = v.tenant_id AND b.id = v.content_blob_id
			WHERE v.tenant_id = $1 AND v.id = $2 AND b.shredded_at IS NULL
		`, tenantID, versionID).Scan(
			&bucket, &key, &mimeType, &kekID, &encryptedDEK, &dekNonce,
		)
	})
	if err != nil {
		http.Error(w, "version or blob not found", http.StatusNotFound)
		return
	}

	obj, err := h.s3.GetObject(r.Context(), bucket, key)
	if err != nil {
		h.log.Error().Err(err).
			Str("bucket", bucket).Str("key", key).
			Msg("decrypt-stream: s3 get failed")
		http.Error(w, "storage fetch failed", http.StatusBadGateway)
		return
	}
	defer func() { _ = obj.Close() }()

	w.Header().Set("Content-Type", mimeType)
	// BUG-05: react-pdf and image viewers rely on Content-Length
	// for progress + on inline disposition to not get prompted as
	// a download. Pass-through previously emitted neither, which
	// looked like a hang on slow connections.
	w.Header().Set("Content-Disposition", "inline")
	// Caching off — the URL itself doesn't carry version info beyond
	// the path, and a re-encrypted blob would serve stale plaintext.
	w.Header().Set("Cache-Control", "no-store")

	// Unencrypted path: pass-through. AES-GCM never decorated this
	// object so we just copy raw bytes to the client.
	if len(encryptedDEK) == 0 {
		if _, err := io.Copy(w, obj); err != nil {
			h.log.Warn().Err(err).Msg("decrypt-stream: passthrough copy failed")
		}
		return
	}

	// Encrypted path: unwrap DEK with tenant KEK, then GCM decrypt.
	if h.kms == nil {
		h.log.Error().Msg("decrypt-stream: kms not configured for encrypted blob")
		http.Error(w, "decryption not configured", http.StatusServiceUnavailable)
		return
	}
	plainDEK, err := h.kms.DecryptDataKey(r.Context(), kekID, encryptedDEK)
	if err != nil {
		h.log.Error().Err(err).Str("kek_id", kekID).
			Msg("decrypt-stream: dek unwrap failed")
		http.Error(w, "dek unwrap failed", http.StatusInternalServerError)
		return
	}
	defer func() {
		for i := range plainDEK {
			plainDEK[i] = 0
		}
	}()

	ciphertext, err := io.ReadAll(obj)
	if err != nil {
		h.log.Error().Err(err).Msg("decrypt-stream: ciphertext read failed")
		http.Error(w, "ciphertext read failed", http.StatusBadGateway)
		return
	}
	plaintext, err := pkgcrypto.DecryptData(ciphertext, dekNonce, plainDEK)
	if err != nil {
		// Auth tag mismatch = tampering or wrong KEK. Don't echo
		// details to the client; log + return generic 500.
		h.log.Error().Err(err).Msg("decrypt-stream: gcm decrypt failed")
		http.Error(w, "decrypt failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(plaintext)))
	if _, err := w.Write(plaintext); err != nil {
		h.log.Warn().Err(err).Msg("decrypt-stream: plaintext write failed")
	}
}
