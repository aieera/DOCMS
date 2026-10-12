// zt_share_handler — zero-trust view-only share (ADR 0098 §18 F2).
//
// Five endpoints:
//
//	POST /api/v1/admin/share-links/zt
//	  — sender creates a token. Body: { document_id, version_id, recipient_email, expires_in_hours, max_views }.
//	  Returns: { token_id, share_url, expires_at }.
//	  Auth: SessionAuth + RequireRole(owner, admin) at the mount site.
//
//	POST /api/v1/admin/share-links/zt/{token_id}/revoke
//	  — sender or admin terminates the session immediately.
//
//	GET  /api/v1/admin/share-links/zt/{token_id}/telemetry
//	  — sender pulls the activity timeline. Returns [{event_type, page_number, dwell_ms, created_at}, …]
//
//	GET  /api/v1/zt/{token}/manifest
//	  — public via token. Returns { page_count, mime_type, watermark_text, sender_name, expires_at, revoked }.
//
//	GET  /api/v1/zt/{token}/stream
//	  — public via token. Streams the document bytes back. Recipient's pdf.js renders client-side; the
//	    watermark is a CSS overlay applied by the viewer page. Per ADR 0098 § Honesty about encryption,
//	    server-side per-tile encryption is wired but feature-flagged because the screenshot threat
//	    isn't defeated by ANY in-browser control without DRM.
//
//	POST /api/v1/zt/{token}/telemetry
//	  — public via token. Recipient page-view / scroll / focus events. Inserted into zt_share_telemetry.
//
// Tokens (D-9, migration 000113): {token} in the public URLs is a random
// pre-image that is NEVER stored; zt_share_tokens keeps only
// token_hash = sha256(token || pepper). token_id is a separate random row id
// — the sender's handle for the admin routes, not a credential. The public
// routes resolve the hash through the SECURITY DEFINER function
// zt_share_lookup_by_token_hash (the tenant is learned from the row) and do
// every other read/write inside database.WithTenantTx.
package handler

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/pkg/auth"
	pkgcrypto "github.com/aieera/sedoc/pkg/crypto"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/storage"
)

// ZTShareHandler exposes the routes above. The recipient byte path reads
// the blob directly (see serveVersionBytes).
type ZTShareHandler struct {
	pool *pgxpool.Pool
	// s3 + kms serve the token-authorized byte path (stream): the
	// recipient has no session, so the bytes are read and decrypted here
	// rather than through the session-oriented storage proxy.
	s3  *storage.S3Client
	kms pkgcrypto.KeyManager
	// pepper is mixed into the SHA-256 of the URL token when computing
	// token_hash. The token is a random UUIDv4 (122 bits); the pepper means
	// a leaked table alone cannot be brute-forced back to live URLs.
	pepper []byte
}

func NewZTShareHandler(pool *pgxpool.Pool, s3 *storage.S3Client, kms pkgcrypto.KeyManager, pepper []byte) *ZTShareHandler {
	return &ZTShareHandler{pool: pool, s3: s3, kms: kms, pepper: pepper}
}

// RegisterAdmin mounts the admin/sender routes (require SessionAuth at the caller).
func (h *ZTShareHandler) RegisterAdmin(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/admin/share-links/zt", h.create)
	mux.HandleFunc("POST /api/v1/admin/share-links/zt/{token_id}/revoke", h.revoke)
	mux.HandleFunc("GET /api/v1/admin/share-links/zt/{token_id}/telemetry", h.telemetryRead)
}

// RegisterPublic mounts the recipient routes — no auth, the token IS the auth.
func (h *ZTShareHandler) RegisterPublic(mux *http.ServeMux) {
	// The path wildcard keeps its historical name; its value is the URL
	// token (pre-image), not the stored token_id.
	mux.HandleFunc("GET /api/v1/zt/{token_id}/manifest", h.manifest)
	mux.HandleFunc("GET /api/v1/zt/{token_id}/stream", h.stream)
	mux.HandleFunc("POST /api/v1/zt/{token_id}/telemetry", h.telemetryWrite)
}

// ---- create -------------------------------------------------------

type createReq struct {
	DocumentID     string `json:"document_id"`
	VersionID      string `json:"version_id"`
	RecipientEmail string `json:"recipient_email"`
	ExpiresInHours int    `json:"expires_in_hours"`
	MaxViews       int    `json:"max_views"`
}

type createResp struct {
	TokenID   string `json:"token_id"`
	ShareURL  string `json:"share_url"`
	ExpiresAt string `json:"expires_at"`
}

func (h *ZTShareHandler) create(w http.ResponseWriter, r *http.Request) {
	tid, terr := auth.GetTenantID(r.Context())
	uid, uerr := tenantOwnerOrFail(r, w)
	if terr != nil || uerr != nil {
		return
	}

	var in createReq
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid json"})
		return
	}
	docID, err := uuid.Parse(in.DocumentID)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "document_id not a uuid"})
		return
	}
	verID, err := uuid.Parse(in.VersionID)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "version_id not a uuid"})
		return
	}
	if in.RecipientEmail == "" {
		writeJSON(w, 400, map[string]string{"error": "recipient_email required"})
		return
	}
	if in.ExpiresInHours <= 0 {
		in.ExpiresInHours = 72
	}
	expiresAt := time.Now().Add(time.Duration(in.ExpiresInHours) * time.Hour)

	// D-9: the URL credential is a random pre-image that is never stored;
	// the row keeps only its peppered hash. token_id is an unrelated random
	// row id (the sender's handle for revoke/telemetry).
	urlToken := uuid.New()
	tokenID := uuid.New()
	tokenHash := h.hashToken(urlToken)
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		writeJSON(w, 500, map[string]string{"error": "rand"})
		return
	}
	watermark := fmt.Sprintf("%s · %s · share=%s", in.RecipientEmail,
		time.Now().UTC().Format("2006-01-02 15:04"), tokenID.String()[:8])

	err = database.WithTenantTx(r.Context(), h.pool, tid, func(tx pgx.Tx) error {
		if _, e := tx.Exec(r.Context(), `
			INSERT INTO zt_share_tokens
				(tenant_id, token_id, token_hash, document_id, version_id,
				 recipient_email, watermark_text, created_by, expires_at,
				 max_views, session_key_seed)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			tid, tokenID, tokenHash, docID, verID,
			in.RecipientEmail, watermark, uid, expiresAt,
			in.MaxViews, seed,
		); e != nil {
			return e
		}
		// Track 8b — the ZT handler previously emitted no event, so no
		// notification was ever delivered for a zero-trust share. Notify the
		// sender (an in-app confirmation; the external recipient gets the link
		// by email). dms.notify.> reaches the notification consumer.
		payload, _ := json.Marshal(map[string]any{
			"tenant_id":     tid.String(),
			"user_ids":      []string{uid.String()},
			"type":          "document.shared",
			"title":         "Secure share sent",
			"body":          fmt.Sprintf("You sent a zero-trust view-only share to %s.", in.RecipientEmail),
			"resource_type": "document",
			"resource_id":   docID.String(),
		})
		evt := database.NewOutboxEvent(tid, "dms.notify.document_shared.v1", "document", docID, payload)
		return database.NewOutboxRepository().Insert(r.Context(), tx, evt)
	})
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "insert", "detail": err.Error()})
		return
	}

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	resp := createResp{
		TokenID:   tokenID.String(),
		ShareURL:  fmt.Sprintf("%s://%s/zt/%s", scheme, r.Host, urlToken.String()),
		ExpiresAt: expiresAt.UTC().Format(time.RFC3339),
	}
	writeJSON(w, 200, resp)
}

// ---- revoke -------------------------------------------------------

func (h *ZTShareHandler) revoke(w http.ResponseWriter, r *http.Request) {
	tid, terr := auth.GetTenantID(r.Context())
	if terr != nil {
		writeJSON(w, 401, map[string]string{"error": "no tenant"})
		return
	}
	callerID, uerr := auth.GetUserID(r.Context())
	if uerr != nil {
		writeJSON(w, 401, map[string]string{"error": "no user"})
		return
	}
	callerRole := auth.GetUserRole(r.Context())
	tokenID, err := uuid.Parse(r.PathValue("token_id"))
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "token_id not a uuid"})
		return
	}
	// Ownership check: only the share creator OR a tenant admin may
	// revoke. SessionAuth already gated tenant scope; this guards
	// against a member yanking the CEO's outbound link.
	err = database.WithTenantTx(r.Context(), h.pool, tid, func(tx pgx.Tx) error {
		predicate := "tenant_id = $1 AND token_id = $2 AND revoked_at IS NULL"
		args := []any{tid, tokenID}
		if !isAdminLikeRole(callerRole) {
			predicate += " AND created_by = $3"
			args = append(args, callerID)
		}
		ct, e := tx.Exec(r.Context(),
			"UPDATE zt_share_tokens SET revoked_at = NOW() WHERE "+predicate,
			args...)
		if e != nil {
			return e
		}
		if ct.RowsAffected() == 0 {
			// Could be: token not found, already revoked, OR caller
			// doesn't own it. Return the same 404 in all three cases
			// so we don't leak existence to non-owners.
			return errors.New("not_found_or_already_revoked")
		}
		return nil
	})
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "not_found_or_already_revoked"})
		return
	}
	w.WriteHeader(204)
}

// isAdminLikeRole — owner/admin/compliance_officer may act on any
// share in their tenant. Mirrors the canRerun pattern in
// documents/$documentId.tsx and the storage-proxy role gate.
func isAdminLikeRole(role string) bool {
	switch role {
	case "owner", "admin", "compliance_officer":
		return true
	}
	return false
}

// ---- manifest (public) --------------------------------------------

type manifestResp struct {
	TokenID       string `json:"token_id"`
	DocumentTitle string `json:"document_title"`
	MimeType      string `json:"mime_type"`
	PageCount     int    `json:"page_count"` // 0 if unknown; viewer uses pdf.js's own counter
	WatermarkText string `json:"watermark_text"`
	SenderName    string `json:"sender_name"`
	ExpiresAt     string `json:"expires_at"`
	Revoked       bool   `json:"revoked"`
	MaxViews      int    `json:"max_views"`
	ViewCount     int    `json:"view_count"`
}

func (h *ZTShareHandler) manifest(w http.ResponseWriter, r *http.Request) {
	tid, tokenID, ok := h.resolveToken(w, r)
	if !ok {
		return
	}
	var (
		watermark, title, mime, sender string
		expiresAt                      time.Time
		revokedAt                      *time.Time
		maxViews, viewCount            int
	)
	err := database.WithTenantTx(r.Context(), h.pool, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `
			SELECT t.watermark_text, t.expires_at, t.revoked_at, t.max_views, t.view_count,
			       d.title, v.mime_type,
			       COALESCE(u.email, '(unknown)')
			FROM zt_share_tokens t
			JOIN documents d ON d.tenant_id = t.tenant_id AND d.id = t.document_id
			JOIN document_versions v ON v.tenant_id = t.tenant_id AND v.id = t.version_id
			LEFT JOIN users u ON u.tenant_id = t.tenant_id AND u.id = t.created_by
			WHERE t.tenant_id = $1 AND t.token_id = $2
		`, tid, tokenID).Scan(&watermark, &expiresAt, &revokedAt, &maxViews, &viewCount,
			&title, &mime, &sender)
	})
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "not_found"})
		return
	}
	if time.Now().After(expiresAt) {
		writeJSON(w, 410, map[string]string{"error": "expired"})
		return
	}
	resp := manifestResp{
		TokenID:       tokenID.String(),
		DocumentTitle: title,
		MimeType:      mime,
		PageCount:     0,
		WatermarkText: watermark,
		SenderName:    sender,
		ExpiresAt:     expiresAt.UTC().Format(time.RFC3339),
		Revoked:       revokedAt != nil,
		MaxViews:      maxViews,
		ViewCount:     viewCount,
	}
	writeJSON(w, 200, resp)
}

// ---- stream (public) ----------------------------------------------

// stream resolves the token, increments view_count, and streams the
// blob bytes back. The watermark is applied client-side as a CSS
// overlay (ADR 0098 § Honesty about encryption — burning into the
// pixels doesn't help against a screenshot anyway).
func (h *ZTShareHandler) stream(w http.ResponseWriter, r *http.Request) {
	tid, tokenID, ok := h.resolveToken(w, r)
	if !ok {
		return
	}

	var (
		docID, verID    uuid.UUID
		mime            string
		expiresAt       time.Time
		revokedAt       *time.Time
		maxViews, views int
	)
	err := database.WithTenantTx(r.Context(), h.pool, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `
			SELECT t.document_id, t.version_id, t.expires_at,
			       t.revoked_at, t.max_views, t.view_count, v.mime_type
			FROM zt_share_tokens t
			JOIN document_versions v ON v.tenant_id = t.tenant_id AND v.id = t.version_id
			WHERE t.tenant_id = $1 AND t.token_id = $2
		`, tid, tokenID).Scan(&docID, &verID, &expiresAt, &revokedAt,
			&maxViews, &views, &mime)
	})
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "not_found"})
		return
	}
	if revokedAt != nil {
		writeJSON(w, 410, map[string]string{"error": "revoked"})
		return
	}
	if time.Now().After(expiresAt) {
		writeJSON(w, 410, map[string]string{"error": "expired"})
		return
	}
	if maxViews > 0 && views >= maxViews {
		writeJSON(w, 410, map[string]string{"error": "view_limit_reached"})
		return
	}

	// Bump counter inside tenant tx so RLS is happy.
	_ = database.WithTenantTx(r.Context(), h.pool, tid, func(tx pgx.Tx) error {
		_, _ = tx.Exec(r.Context(),
			`UPDATE zt_share_tokens SET view_count = view_count + 1
			 WHERE tenant_id = $1 AND token_id = $2`, tid, tokenID)
		return nil
	})

	h.serveVersionBytes(w, r, tid, docID, verID, mime)
}

// serveVersionBytes is the token-authorized read path (D-4). The recipient
// has no session, so the session-oriented storage proxy (per-user policy
// check; encrypted blobs redirected to the session-only decrypt-stream
// route) cannot serve them. The token row already pins (document, version)
// and the caller has passed the token gates, so the blob is loaded inside
// the token's tenant transaction (version constrained to the shared
// document), fetched from object storage, and — when envelope-encrypted —
// unwrapped under the tenant KEK and decrypted server-side. The storage
// location never reaches the recipient.
func (h *ZTShareHandler) serveVersionBytes(w http.ResponseWriter, r *http.Request, tid, docID, verID uuid.UUID, versionMime string) {
	if h.s3 == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "storage_unavailable"})
		return
	}
	var (
		bucket, key, mime, kekID string
		encryptedDEK, dekNonce   []byte
	)
	err := database.WithTenantTx(r.Context(), h.pool, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `
			SELECT b.storage_bucket, b.storage_key,
			       COALESCE(NULLIF(v.mime_type, ''), NULLIF(b.mime_type, ''), 'application/octet-stream'),
			       COALESCE(b.kek_id, ''), b.encrypted_dek, b.dek_nonce
			FROM document_versions v
			JOIN content_blobs b ON b.tenant_id = v.tenant_id AND b.id = v.content_blob_id
			WHERE v.tenant_id = $1 AND v.document_id = $2 AND v.id = $3 AND b.shredded_at IS NULL
		`, tid, docID, verID).Scan(&bucket, &key, &mime, &kekID, &encryptedDEK, &dekNonce)
	})
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	if mime == "" {
		mime = versionMime
	}
	obj, err := h.s3.GetObject(r.Context(), bucket, key)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "storage_fetch_failed"})
		return
	}
	defer func() { _ = obj.Close() }()

	setZTContentHeaders(w, mime)
	if len(encryptedDEK) == 0 {
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, obj)
		return
	}
	if h.kms == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "decryption_unavailable"})
		return
	}
	plainDEK, err := h.kms.DecryptDataKey(r.Context(), kekID, encryptedDEK)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "decrypt_failed"})
		return
	}
	defer zero(plainDEK)
	// GCM verifies the tag only after the whole ciphertext is read; bound
	// the buffer exactly like the anonymous share-link download path.
	maxBytes := defaultShareDecryptMaxBytes
	if v := os.Getenv("SEDOC_SHARE_DECRYPT_MAX_BYTES"); v != "" {
		if n, perr := strconv.ParseInt(v, 10, 64); perr == nil && n > 0 {
			maxBytes = n
		}
	}
	ciphertext, err := io.ReadAll(io.LimitReader(obj, maxBytes+1))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "storage_read_failed"})
		return
	}
	if int64(len(ciphertext)) > maxBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "too_large"})
		return
	}
	plaintext, err := pkgcrypto.DecryptData(ciphertext, dekNonce, plainDEK)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "decrypt_failed"})
		return
	}
	defer zero(plaintext)
	w.Header().Set("Content-Length", strconv.Itoa(len(plaintext)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(plaintext)
}

// setZTContentHeaders: per-recipient bytes are never cached; only media
// that cannot execute script renders inline (same XSS guard as the
// watermarked download).
func setZTContentHeaders(w http.ResponseWriter, mime string) {
	w.Header().Set("Content-Type", mime)
	if inlineSafeMime(mime) {
		w.Header().Set("Content-Disposition", "inline")
	} else {
		w.Header().Set("Content-Disposition", `attachment; filename="document"`)
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("Cache-Control", "private, no-store, max-age=0")
}

// ---- telemetry (public write) -------------------------------------

type telemetryWriteReq struct {
	EventType  string `json:"event_type"`
	PageNumber *int   `json:"page_number,omitempty"`
	DwellMS    *int   `json:"dwell_ms,omitempty"`
}

func (h *ZTShareHandler) telemetryWrite(w http.ResponseWriter, r *http.Request) {
	tid, tokenID, ok := h.resolveToken(w, r)
	if !ok {
		return
	}

	var in telemetryWriteReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid json"})
		return
	}
	if !validEventType(in.EventType) {
		writeJSON(w, 400, map[string]string{"error": "invalid event_type"})
		return
	}

	ipHash := h.hashIP(r, tid)
	ua := r.Header.Get("User-Agent")
	if len(ua) > 256 {
		ua = ua[:256]
	}

	_ = database.WithTenantTx(r.Context(), h.pool, tid, func(tx pgx.Tx) error {
		_, _ = tx.Exec(r.Context(), `
			INSERT INTO zt_share_telemetry
				(tenant_id, token_id, event_type, page_number, dwell_ms, user_agent, ip_hash)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			tid, tokenID, in.EventType, in.PageNumber, in.DwellMS, ua, ipHash)
		return nil
	})

	w.WriteHeader(204)
}

// ---- telemetry (sender read) --------------------------------------

func (h *ZTShareHandler) telemetryRead(w http.ResponseWriter, r *http.Request) {
	tid, terr := auth.GetTenantID(r.Context())
	if terr != nil {
		writeJSON(w, 401, map[string]string{"error": "no tenant"})
		return
	}
	callerID, uerr := auth.GetUserID(r.Context())
	if uerr != nil {
		writeJSON(w, 401, map[string]string{"error": "no user"})
		return
	}
	callerRole := auth.GetUserRole(r.Context())
	tokenID, err := uuid.Parse(r.PathValue("token_id"))
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "token_id not a uuid"})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	// Ownership check: telemetry exposes view counts, IP fingerprints,
	// and screenshot-attempt events. Only the share creator (or a
	// tenant admin) gets to see them — a non-owner could otherwise
	// monitor when the CEO's confidential share is being viewed.
	if !isAdminLikeRole(callerRole) {
		var createdBy uuid.UUID
		err := database.WithTenantTx(r.Context(), h.pool, tid, func(tx pgx.Tx) error {
			return tx.QueryRow(r.Context(),
				`SELECT created_by FROM zt_share_tokens WHERE tenant_id = $1 AND token_id = $2`,
				tid, tokenID).Scan(&createdBy)
		})
		if err != nil {
			writeJSON(w, 404, map[string]string{"error": "not_found"})
			return
		}
		if createdBy != callerID {
			// 404 (not 403) so we don't leak existence of someone
			// else's share token to a non-owner.
			writeJSON(w, 404, map[string]string{"error": "not_found"})
			return
		}
	}

	type evt struct {
		EventType  string  `json:"event_type"`
		PageNumber *int    `json:"page_number,omitempty"`
		DwellMS    *int    `json:"dwell_ms,omitempty"`
		CreatedAt  string  `json:"created_at"`
		UserAgent  *string `json:"user_agent,omitempty"`
	}
	out := make([]evt, 0, limit)

	err = database.WithTenantTx(r.Context(), h.pool, tid, func(tx pgx.Tx) error {
		rows, e := tx.Query(r.Context(), `
			SELECT event_type, page_number, dwell_ms, user_agent, created_at
			FROM zt_share_telemetry
			WHERE tenant_id = $1 AND token_id = $2
			ORDER BY created_at DESC LIMIT $3`,
			tid, tokenID, limit)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var e evt
			var ts time.Time
			if err := rows.Scan(&e.EventType, &e.PageNumber, &e.DwellMS, &e.UserAgent, &ts); err != nil {
				return err
			}
			e.CreatedAt = ts.UTC().Format(time.RFC3339)
			out = append(out, e)
		}
		return rows.Err()
	})
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "query"})
		return
	}
	writeJSON(w, 200, out)
}

// ---- helpers ------------------------------------------------------

// resolveToken maps the URL token (pre-image) to the owning tenant and the
// stored row id. The tenant is unknown until the row is found and
// zt_share_tokens is FORCE RLS, so this is the one pre-tenant read: the
// SECURITY DEFINER lookup from migration 000113, exact match on the
// peppered hash. Writes a uniform 404 and returns ok=false for malformed or
// unknown tokens.
func (h *ZTShareHandler) resolveToken(w http.ResponseWriter, r *http.Request) (tid, tokenID uuid.UUID, ok bool) {
	urlToken, err := uuid.Parse(r.PathValue("token_id"))
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "not_found"})
		return uuid.Nil, uuid.Nil, false
	}
	err = h.pool.QueryRow(r.Context(),
		`SELECT tenant_id, token_id FROM zt_share_lookup_by_token_hash($1)`,
		h.hashToken(urlToken)).Scan(&tid, &tokenID)
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "not_found"})
		return uuid.Nil, uuid.Nil, false
	}
	return tid, tokenID, true
}

func (h *ZTShareHandler) hashToken(id uuid.UUID) []byte {
	hh := sha256.New()
	hh.Write(id[:])
	hh.Write(h.pepper)
	return hh.Sum(nil)
}

func (h *ZTShareHandler) hashIP(r *http.Request, tid uuid.UUID) string {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip == "" {
		ip = r.RemoteAddr
	}
	hh := sha256.New()
	hh.Write([]byte(ip))
	hh.Write(tid[:])
	hh.Write(h.pepper)
	return hex.EncodeToString(hh.Sum(nil))
}

func validEventType(s string) bool {
	switch s {
	case "page_view", "scroll", "focus_blur", "devtools_open":
		return true
	}
	return false
}

// tenantOwnerOrFail extracts auth context; returns user_id or 401s.
func tenantOwnerOrFail(r *http.Request, w http.ResponseWriter) (uuid.UUID, error) {
	u, err := auth.User(r.Context())
	if err != nil || u.ID == uuid.Nil {
		writeJSON(w, 401, map[string]string{"error": "authentication required"})
		return uuid.Nil, err
	}
	return u.ID, nil
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
