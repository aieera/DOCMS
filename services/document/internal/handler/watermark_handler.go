// Dynamic viewer watermark handler (§5).
//
//	GET /api/v1/documents/{document_id}/versions/{version_id}/wm/status
//	GET /api/v1/documents/{document_id}/versions/{version_id}/wm/pages/{page_number}
//	GET /api/v1/documents/{document_id}/versions/{version_id}/wm/thumbnail
//	GET /api/v1/documents/{document_id}/versions/{version_id}/wm/download
//
// The document service authorizes (EnsureCanViewDocument), resolves the
// tenant/classification watermark style, substitutes the viewer's identity into
// the template, and delegates the drawing to services/preview (which owns the
// render libs + cached base pages). Two viewers therefore see their own
// identity on the same page. Download/print burn-in is policy-gated on
// classification.
package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/auth"
	pkgcrypto "github.com/aieera/sedoc/pkg/crypto"
	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/pkg/storage"
	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/service"
	"github.com/aieera/sedoc/services/document/internal/watermark"
)

var errStorageUnavailable = errors.New("storage not configured")

// WatermarkHandler serves the watermarked viewer + download/print surfaces.
type WatermarkHandler struct {
	svc     *service.DocumentService
	preview *watermark.PreviewClient
	pool    *pgxpool.Pool
	s3      *storage.S3Client
	kms     pkgcrypto.KeyManager
	log     zerolog.Logger
}

func NewWatermarkHandler(svc *service.DocumentService, preview *watermark.PreviewClient, pool *pgxpool.Pool, s3 *storage.S3Client, kms pkgcrypto.KeyManager, log zerolog.Logger) *WatermarkHandler {
	return &WatermarkHandler{svc: svc, preview: preview, pool: pool, s3: s3, kms: kms, log: log}
}

func (h *WatermarkHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/documents/{document_id}/versions/{version_id}/wm/status", h.status)
	mux.HandleFunc("GET /api/v1/documents/{document_id}/versions/{version_id}/wm/pages/{page_number}", h.page)
	mux.HandleFunc("GET /api/v1/documents/{document_id}/versions/{version_id}/wm/thumbnail", h.thumbnail)
	mux.HandleFunc("GET /api/v1/documents/{document_id}/versions/{version_id}/wm/download", h.download)
}

// ---- shared preamble ----------------------------------------------------

type wmReq struct {
	tenantID, userID, docID, versionID uuid.UUID
}

// auth + parse + EnsureCanViewDocument. Writes the error response itself and
// returns ok=false on failure.
func (h *WatermarkHandler) preamble(w http.ResponseWriter, r *http.Request) (wmReq, bool) {
	var out wmReq
	tenantID, err := auth.GetTenantID(r.Context())
	if err != nil || tenantID == uuid.Nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return out, false
	}
	userID, _ := auth.GetUserID(r.Context())
	docID, err := uuid.Parse(r.PathValue("document_id"))
	if err != nil {
		http.Error(w, "document_id not a uuid", http.StatusBadRequest)
		return out, false
	}
	versionID, err := uuid.Parse(r.PathValue("version_id"))
	if err != nil {
		http.Error(w, "version_id not a uuid", http.StatusBadRequest)
		return out, false
	}
	if err := h.svc.EnsureCanViewDocument(r.Context(), docID); err != nil {
		writeErr(w, r, err)
		return out, false
	}
	out = wmReq{tenantID: tenantID, userID: userID, docID: docID, versionID: versionID}
	return out, true
}

// buildText resolves the final watermark text for this viewer, or "" when the
// watermark is disabled for this document (the page is still served, just
// unstamped, so the viewer renders consistently).
func (h *WatermarkHandler) buildText(r *http.Request, req wmReq, eff model.EffectiveWatermark, doc *model.Document) string {
	if !eff.Enabled {
		return ""
	}
	email := auth.GetUserName(r.Context())
	if email == "" {
		email = req.userID.String()
	}
	cls := doc.SecurityClassification
	if cls == "" {
		cls = model.ClassUnclassified
	}
	if doc.HasPHI {
		cls += " · PHI"
	}
	return model.SubstituteWatermarkTokens(eff.Template, map[string]string{
		"email":          email,
		"user_id":        req.userID.String(),
		"ip":             auth.GetClientIP(r.Context()),
		"tenant":         req.tenantID.String(),
		"timestamp":      time.Now().UTC().Format("2006-01-02 15:04 MST"),
		"classification": cls,
	})
}

// ---- handlers -----------------------------------------------------------

func (h *WatermarkHandler) status(w http.ResponseWriter, r *http.Request) {
	req, ok := h.preamble(w, r)
	if !ok {
		return
	}
	eff, _, err := h.svc.ResolveWatermarkForDoc(r.Context(), req.docID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	resp := map[string]any{"status": "none", "page_count": nil, "watermark_enabled": eff.Enabled}
	if h.preview.Enabled() {
		st, serr := h.preview.Status(r.Context(), req.tenantID.String(), req.docID.String(), req.versionID.String())
		if serr != nil {
			h.log.Warn().Err(serr).Msg("watermark: preview status failed")
		} else {
			resp["status"] = st.Status
			resp["page_count"] = st.PageCount
		}
	}
	writeJSONStatus(w, http.StatusOK, resp)
}

func (h *WatermarkHandler) page(w http.ResponseWriter, r *http.Request) {
	h.stampImage(w, r, false)
}

func (h *WatermarkHandler) thumbnail(w http.ResponseWriter, r *http.Request) {
	h.stampImage(w, r, true)
}

func (h *WatermarkHandler) stampImage(w http.ResponseWriter, r *http.Request, thumbnail bool) {
	req, ok := h.preamble(w, r)
	if !ok {
		return
	}
	if !h.preview.Enabled() {
		http.Error(w, "watermark rendering unavailable", http.StatusServiceUnavailable)
		return
	}
	pageNum := 1
	if !thumbnail {
		n, err := strconv.Atoi(r.PathValue("page_number"))
		if err != nil || n < 1 {
			http.Error(w, "page_number invalid", http.StatusBadRequest)
			return
		}
		pageNum = n
	}
	eff, doc, err := h.svc.ResolveWatermarkForDoc(r.Context(), req.docID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	png, status, err := h.preview.StampPage(r.Context(), watermark.PageStampRequest{
		TenantID:    req.tenantID.String(),
		DocumentID:  req.docID.String(),
		VersionID:   req.versionID.String(),
		PageNumber:  pageNum,
		Thumbnail:   thumbnail,
		Text:        h.buildText(r, req, eff, doc),
		Opacity:     eff.Opacity,
		RotationDeg: eff.RotationDeg,
		Tile:        eff.Tile,
		FontSize:    eff.FontSize,
		Color:       eff.Color,
	})
	if err != nil {
		if status == http.StatusNotFound {
			http.Error(w, "preview not ready", http.StatusNotFound)
			return
		}
		h.log.Error().Err(err).Msg("watermark: page stamp failed")
		http.Error(w, "watermark render failed", http.StatusBadGateway)
		return
	}
	// Per-viewer artifact: never cache or share across users. `private` +
	// Vary: Cookie harden against non-compliant intermediary caches keying on
	// the URL alone (the session cookie is what distinguishes viewers).
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, no-store, max-age=0")
	w.Header().Set("Vary", "Cookie")
	w.Header().Set("Content-Length", strconv.Itoa(len(png)))
	_, _ = w.Write(png)
}

// download streams a watermarked PDF for print/download. Burn-in is policy-gated
// on classification (confidential/restricted/PHI); other documents stream the
// plaintext unchanged. Non-PDF documents are never PDF-stamped.
func (h *WatermarkHandler) download(w http.ResponseWriter, r *http.Request) {
	req, ok := h.preamble(w, r)
	if !ok {
		return
	}
	eff, doc, err := h.svc.ResolveWatermarkForDoc(r.Context(), req.docID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	plaintext, mime, err := h.readVersionPlaintext(r, req.tenantID, req.docID, req.versionID)
	if err != nil {
		writeErr(w, r, err)
		return
	}

	// "where required" gate: when the watermark is enabled AND the document is
	// sensitive (confidential/restricted/PHI), the download MUST carry the
	// watermark. If we cannot stamp it — preview disabled, a non-PDF we can't
	// burn into, or a stamp failure — fail CLOSED rather than hand out an
	// unwatermarked confidential/PHI copy. Non-gated or watermark-disabled
	// documents stream plainly.
	watermarked := false
	out := plaintext
	mustWatermark := eff.Enabled && model.WatermarkDownloadGated(doc.SecurityClassification, doc.HasPHI)
	if mustWatermark {
		if mime != "application/pdf" || !h.preview.Enabled() {
			h.log.Error().Str("mime", mime).Bool("preview_enabled", h.preview.Enabled()).
				Msg("watermark: cannot stamp gated document; refusing unwatermarked download")
			http.Error(w, "watermark required but unavailable for this document", http.StatusServiceUnavailable)
			return
		}
		stamped, status, serr := h.preview.StampPDF(r.Context(), plaintext,
			h.buildText(r, req, eff, doc), eff.Opacity, eff.RotationDeg, eff.Tile, eff.FontSize, eff.Color)
		if serr != nil {
			h.log.Error().Err(serr).Int("preview_status", status).Msg("watermark: pdf burn-in failed for gated doc")
			http.Error(w, "watermark render failed", http.StatusBadGateway)
			return
		}
		out = stamped
		watermarked = true
	} else if eff.Enabled && mime == "application/pdf" && h.preview.Enabled() {
		// Non-gated but watermark enabled: best-effort stamp; on failure fall
		// back to the plain (authorized) copy rather than blocking the download.
		if stamped, _, serr := h.preview.StampPDF(r.Context(), plaintext,
			h.buildText(r, req, eff, doc), eff.Opacity, eff.RotationDeg, eff.Tile, eff.FontSize, eff.Color); serr == nil {
			out = stamped
			watermarked = true
		}
	}

	w.Header().Set("Content-Type", mime)
	// XSS defense: only render trusted media inline; anything else (e.g. a
	// document whose stored mime is text/html or image/svg+xml) is forced to
	// download as an attachment so the browser never executes it in our origin.
	// nosniff + a locked-down CSP sandbox are belt-and-suspenders.
	if inlineSafeMime(mime) {
		w.Header().Set("Content-Disposition", "inline") // print/preview-friendly
	} else {
		w.Header().Set("Content-Disposition", "attachment; filename=\"document\"")
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("Cache-Control", "private, no-store, max-age=0")
	w.Header().Set("Vary", "Cookie")
	w.Header().Set("Content-Length", strconv.Itoa(len(out)))
	_, _ = w.Write(out)
	h.emitDownloadAudit(r, req.tenantID, req.docID, req.versionID, watermarked)
}

// inlineSafeMime reports whether a media type is safe to serve with an inline
// disposition (cannot execute script in our origin). Notably excludes text/html
// and image/svg+xml.
func inlineSafeMime(mime string) bool {
	switch mime {
	case "application/pdf", "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	}
	return false
}

// ---- helpers ------------------------------------------------------------

// readVersionPlaintext loads a version's blob and returns the plaintext bytes +
// mime, decrypting envelope-encrypted blobs. The version is constrained to the
// authorized document (v.document_id = docID): the caller authorized docID via
// EnsureCanViewDocument, and version ids are only tenant-unique, so without this
// predicate a viewer of document A could pass B's version id and read B's bytes
// (cross-document IDOR).
func (h *WatermarkHandler) readVersionPlaintext(r *http.Request, tenantID, docID, versionID uuid.UUID) ([]byte, string, error) {
	if h.s3 == nil {
		return nil, "", errStorageUnavailable
	}
	var (
		bucket, key, mimeType, kekID string
		encryptedDEK, dekNonce       []byte
	)
	err := database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `
			SELECT b.storage_bucket, b.storage_key,
			       COALESCE(NULLIF(v.mime_type, ''), NULLIF(b.mime_type, ''), 'application/octet-stream'),
			       COALESCE(b.kek_id, ''), b.encrypted_dek, b.dek_nonce
			FROM document_versions v
			JOIN content_blobs b ON b.tenant_id = v.tenant_id AND b.id = v.content_blob_id
			WHERE v.tenant_id = $1 AND v.document_id = $2 AND v.id = $3 AND b.shredded_at IS NULL
		`, tenantID, docID, versionID).Scan(&bucket, &key, &mimeType, &kekID, &encryptedDEK, &dekNonce)
	})
	if err != nil {
		return nil, "", vdmserr.ErrNotFound
	}
	obj, err := h.s3.GetObject(r.Context(), bucket, key)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = obj.Close() }()
	ciphertext, err := io.ReadAll(obj)
	if err != nil {
		return nil, "", err
	}
	if len(encryptedDEK) == 0 {
		return ciphertext, mimeType, nil // unencrypted pass-through
	}
	if h.kms == nil {
		return nil, "", errStorageUnavailable
	}
	plainDEK, err := h.kms.DecryptDataKey(r.Context(), kekID, encryptedDEK)
	if err != nil {
		return nil, "", err
	}
	defer func() {
		for i := range plainDEK {
			plainDEK[i] = 0
		}
	}()
	plaintext, err := pkgcrypto.DecryptData(ciphertext, dekNonce, plainDEK)
	if err != nil {
		return nil, "", err
	}
	return plaintext, mimeType, nil
}

func (h *WatermarkHandler) emitDownloadAudit(r *http.Request, tenantID, docID, versionID uuid.UUID, watermarked bool) {
	if h.pool == nil {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"tenant_id":   tenantID.String(),
		"document_id": docID.String(),
		"version_id":  versionID.String(),
		"watermarked": watermarked,
		"at":          time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return
	}
	evt := database.NewOutboxEvent(tenantID, "dms.document.downloaded.v1", "document", docID, payload)
	repo := database.NewOutboxRepository()
	_ = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		return repo.Insert(r.Context(), tx, evt)
	})
}
