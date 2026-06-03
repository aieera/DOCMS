// Anonymous share-link download handler — FIX-10 follow-up.
//
//	GET /api/v1/shared/{token}/download[?password=...]
//
// Closes the broken-promise gap from commit 6fe48f5, which dropped
// the deterministic placeholder URL but didn't actually build the
// recipient-download flow. AccessShareLink returns the JSON browse
// view; this handler is the bytes path.
//
// Authority: the share-link token IS the entitlement. There is no
// SessionAuth here. The handler re-validates IsActive / ExpiresAt /
// MaxViews / "download" capability / password atomically with the
// view-count UPDATE — exactly the same gates the JSON path enforces.
// Rate-limiting is owned by the shareLimiter middleware in main.go
// (per-IP token bucket scoped to /api/v1/shared/).
//
// Streaming shape mirrors decrypt_stream.go: unencrypted blobs are
// pass-through; envelope-encrypted blobs are unwrapped + GCM-
// decrypted server-side. Content-Disposition is `attachment` (this
// is a download, not an inline preview) and the filename falls back
// to the document title when present.
package handler

import (
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	pkgcrypto "github.com/aieera/sedoc/pkg/crypto"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/pkg/storage"
	"github.com/aieera/sedoc/services/document/internal/service"
)

// Hard cap on ciphertext we'll buffer for in-memory GCM decrypt on the
// anonymous share endpoint. AES-GCM in the stdlib is single-tag whole-blob,
// so we can't stream-decrypt without re-architecting the encrypt path. Cap
// keeps the OOM surface bounded — large legitimate shares should use
// authenticated sessions where the streaming decrypt path has identity
// gating (rate-limit per user, not per IP). Overridable via env for ops.
const defaultShareDecryptMaxBytes int64 = 100 << 20 // 100 MiB

type ShareDownloadHandler struct {
	pool *pgxpool.Pool
	s3   *storage.S3Client
	kms  pkgcrypto.KeyManager
	svc  *service.DocumentService
	log  zerolog.Logger
}

func NewShareDownloadHandler(
	pool *pgxpool.Pool,
	s3 *storage.S3Client,
	kms pkgcrypto.KeyManager,
	svc *service.DocumentService,
	log zerolog.Logger,
) *ShareDownloadHandler {
	return &ShareDownloadHandler{pool: pool, s3: s3, kms: kms, svc: svc, log: log}
}

func (h *ShareDownloadHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/shared/{token}/download", h.download)
}

func (h *ShareDownloadHandler) download(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		http.Error(w, "token required", http.StatusBadRequest)
		return
	}
	if h.s3 == nil {
		h.log.Error().Msg("share-download: s3 not configured")
		http.Error(w, "storage not configured", http.StatusServiceUnavailable)
		return
	}

	// FE flow: GET without password → 401 with WWW-Authenticate or a
	// JSON hint, then retry with ?password=. Browsers can pass it as
	// a query string directly when the recipient pasted it into the
	// link UI.
	resolution, err := h.svc.ResolveShareDownload(r.Context(), token, r.URL.Query().Get("password"))
	if err != nil {
		// Map error kinds to status codes explicitly so the FE can
		// distinguish "give me a password" from "this link is dead".
		switch {
		case errors.Is(err, vdmserr.ErrNotFound):
			http.Error(w, "link not found", http.StatusNotFound)
		case errors.Is(err, vdmserr.ErrForbidden):
			http.Error(w, "forbidden", http.StatusForbidden)
		default:
			// Conflict for expired / view-count exceeded, generic
			// 500 for the rest. vdmserr.HTTPStatus would also work
			// if/when it lands on every kind.
			http.Error(w, err.Error(), http.StatusConflict)
		}
		return
	}
	if resolution.PasswordRequired {
		// FE prompts for a password; pass it on the retry as
		// ?password=. We do NOT return WWW-Authenticate Basic here
		// because the browser's default UI is jarring for a
		// non-corporate share URL.
		http.Error(w, "password required", http.StatusUnauthorized)
		return
	}

	obj, err := h.s3.GetObject(r.Context(), resolution.Bucket, resolution.Key)
	if err != nil {
		h.log.Error().Err(err).
			Str("bucket", resolution.Bucket).Str("key", resolution.Key).
			Msg("share-download: s3 get failed")
		http.Error(w, "storage fetch failed", http.StatusBadGateway)
		return
	}
	defer func() { _ = obj.Close() }()

	filename := resolution.Filename
	if filename == "" {
		filename = resolution.DocumentID.String()
	}
	w.Header().Set("Content-Type", resolution.MimeType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+sanitizeContentDispositionFilename(filename)+`"`)
	w.Header().Set("Cache-Control", "no-store")

	// Unencrypted pass-through.
	if len(resolution.EncryptedDEK) == 0 {
		if _, err := io.Copy(w, obj); err != nil {
			h.log.Warn().Err(err).Msg("share-download: passthrough copy failed")
		}
		return
	}

	// Encrypted: unwrap DEK with tenant KEK, GCM-decrypt, write
	// plaintext. Mirror decrypt_stream.go's full-buffer model — the
	// auth tag can only be verified after all ciphertext is read.
	if h.kms == nil {
		h.log.Error().Msg("share-download: kms not configured for encrypted blob")
		http.Error(w, "decryption not configured", http.StatusServiceUnavailable)
		return
	}
	plainDEK, err := h.kms.DecryptDataKey(r.Context(), resolution.KEKID, resolution.EncryptedDEK)
	if err != nil {
		h.log.Error().Err(err).Str("kek_id", resolution.KEKID).
			Msg("share-download: dek unwrap failed")
		http.Error(w, "dek unwrap failed", http.StatusInternalServerError)
		return
	}
	defer func() {
		for i := range plainDEK {
			plainDEK[i] = 0
		}
	}()
	// LimitReader caps at maxBytes+1 so we can detect oversize without
	// reading the entire stream. Anything past the cap is rejected with
	// 413 — the user should request access via an authenticated session
	// instead of the anonymous share path.
	maxBytes := defaultShareDecryptMaxBytes
	if v := os.Getenv("VAULTDMS_SHARE_DECRYPT_MAX_BYTES"); v != "" {
		if n, perr := strconv.ParseInt(v, 10, 64); perr == nil && n > 0 {
			maxBytes = n
		}
	}
	ciphertext, err := io.ReadAll(io.LimitReader(obj, maxBytes+1))
	if err != nil {
		h.log.Error().Err(err).Msg("share-download: ciphertext read failed")
		http.Error(w, "ciphertext read failed", http.StatusBadGateway)
		return
	}
	if int64(len(ciphertext)) > maxBytes {
		h.log.Warn().
			Int64("max_bytes", maxBytes).
			Str("bucket", resolution.Bucket).Str("key", resolution.Key).
			Msg("share-download: ciphertext exceeds anonymous decrypt cap")
		http.Error(w,
			"file too large for anonymous share download; request access via signed-in session",
			http.StatusRequestEntityTooLarge)
		return
	}
	plaintext, err := pkgcrypto.DecryptData(ciphertext, resolution.DEKNonce, plainDEK)
	if err != nil {
		// Auth tag mismatch = tampering or wrong KEK. Don't echo
		// details to the client; opaque 500.
		h.log.Error().Err(err).Msg("share-download: gcm decrypt failed")
		http.Error(w, "decrypt failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(plaintext)))
	if _, err := w.Write(plaintext); err != nil {
		h.log.Warn().Err(err).Msg("share-download: plaintext write failed")
	}
}

// sanitizeContentDispositionFilename strips characters that would
// break the `attachment; filename="..."` header. Quotes / newlines
// are the practical risks; non-ASCII is allowed because modern
// browsers parse RFC 6266 UTF-8 directly.
func sanitizeContentDispositionFilename(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '"' || r == '\r' || r == '\n' || r == '\\' {
			continue
		}
		out = append(out, r)
	}
	return string(out)
}
