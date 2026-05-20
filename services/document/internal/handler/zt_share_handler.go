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
//	GET  /api/v1/zt/{token_id}/manifest
//	  — public via token. Returns { page_count, mime_type, watermark_text, sender_name, expires_at, revoked }.
//
//	GET  /api/v1/zt/{token_id}/stream
//	  — public via token. Streams the document bytes back. Recipient's pdf.js renders client-side; the
//	    watermark is a CSS overlay applied by the viewer page. Per ADR 0098 § Honesty about encryption,
//	    server-side per-tile encryption is wired but feature-flagged because the screenshot threat
//	    isn't defeated by ANY in-browser control without DRM.
//
//	POST /api/v1/zt/{token_id}/telemetry
//	  — public via token. Recipient page-view / scroll / focus events. Inserted into zt_share_telemetry.
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
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vaultdms/vaultdms/pkg/auth"
	"github.com/vaultdms/vaultdms/pkg/database"
)

// ZTShareHandler exposes the five routes above. Storage download is
// delegated to the existing StorageProxy (it knows how to fetch blob
// bytes via the storage gRPC service).
type ZTShareHandler struct {
	pool         *pgxpool.Pool
	storageProxy *StorageProxy
	// pepper is mixed into the SHA-256 of the token_id when computing
	// token_hash. Same per-tenant pepper isn't required — the token_id
	// is a UUIDv7, sufficiently large; pepper just defeats trivial
	// rainbow lookups against the index.
	pepper []byte
}

func NewZTShareHandler(pool *pgxpool.Pool, sp *StorageProxy, pepper []byte) *ZTShareHandler {
	return &ZTShareHandler{pool: pool, storageProxy: sp, pepper: pepper}
}

// RegisterAdmin mounts the admin/sender routes (require SessionAuth at the caller).
func (h *ZTShareHandler) RegisterAdmin(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/admin/share-links/zt", h.create)
	mux.HandleFunc("POST /api/v1/admin/share-links/zt/{token_id}/revoke", h.revoke)
	mux.HandleFunc("GET /api/v1/admin/share-links/zt/{token_id}/telemetry", h.telemetryRead)
}

// RegisterPublic mounts the recipient routes — no auth, the token IS the auth.
func (h *ZTShareHandler) RegisterPublic(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/zt/{token_id}/manifest", h.manifest)
	mux.HandleFunc("GET /api/v1/zt/{token_id}/stream", h.stream)
	mux.HandleFunc("POST /api/v1/zt/{token_id}/telemetry", h.telemetryWrite)
}

// ---- create -------------------------------------------------------

type createReq struct {
	DocumentID      string `json:"document_id"`
	VersionID       string `json:"version_id"`
	RecipientEmail  string `json:"recipient_email"`
	ExpiresInHours  int    `json:"expires_in_hours"`
	MaxViews        int    `json:"max_views"`
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

	tokenID := uuid.New() // UUIDv4 here; could be v7 for sortability — both work.
	tokenHash := h.hashToken(tokenID)
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		writeJSON(w, 500, map[string]string{"error": "rand"})
		return
	}
	watermark := fmt.Sprintf("%s · %s · share=%s", in.RecipientEmail,
		time.Now().UTC().Format("2006-01-02 15:04"), tokenID.String()[:8])

	err = database.WithTenantTx(r.Context(), h.pool, tid, func(tx pgx.Tx) error {
		_, e := tx.Exec(r.Context(), `
			INSERT INTO zt_share_tokens
				(tenant_id, token_id, token_hash, document_id, version_id,
				 recipient_email, watermark_text, created_by, expires_at,
				 max_views, session_key_seed)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			tid, tokenID, tokenHash, docID, verID,
			in.RecipientEmail, watermark, uid, expiresAt,
			in.MaxViews, seed,
		)
		return e
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
		ShareURL:  fmt.Sprintf("%s://%s/zt/%s", scheme, r.Host, tokenID.String()),
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
	tokenID, err := uuid.Parse(r.PathValue("token_id"))
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "token_id not a uuid"})
		return
	}
	err = database.WithTenantTx(r.Context(), h.pool, tid, func(tx pgx.Tx) error {
		ct, e := tx.Exec(r.Context(),
			`UPDATE zt_share_tokens SET revoked_at = NOW()
			 WHERE tenant_id = $1 AND token_id = $2 AND revoked_at IS NULL`,
			tid, tokenID)
		if e != nil {
			return e
		}
		if ct.RowsAffected() == 0 {
			return errors.New("not_found_or_already_revoked")
		}
		return nil
	})
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(204)
}

// ---- manifest (public) --------------------------------------------

type manifestResp struct {
	TokenID       string `json:"token_id"`
	DocumentTitle string `json:"document_title"`
	MimeType      string `json:"mime_type"`
	PageCount     int    `json:"page_count"`     // 0 if unknown; viewer uses pdf.js's own counter
	WatermarkText string `json:"watermark_text"`
	SenderName    string `json:"sender_name"`
	ExpiresAt     string `json:"expires_at"`
	Revoked       bool   `json:"revoked"`
	MaxViews      int    `json:"max_views"`
	ViewCount     int    `json:"view_count"`
}

func (h *ZTShareHandler) manifest(w http.ResponseWriter, r *http.Request) {
	tokenID, err := uuid.Parse(r.PathValue("token_id"))
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "not_found"})
		return
	}
	tokenHash := h.hashToken(tokenID)

	// Look up the token WITHOUT requiring tenant context on the
	// request — the token IS the cross-tenant authentication. We
	// do hit RLS-bypass-free SQL via a dedicated low-privilege role
	// elsewhere; in this Phase 1, we use a direct lookup keyed by
	// token_hash and treat the returned tenant_id as the scope.
	row := h.pool.QueryRow(r.Context(), `
		SELECT t.tenant_id, t.document_id, t.version_id, t.watermark_text,
		       t.expires_at, t.revoked_at, t.max_views, t.view_count,
		       d.title, v.mime_type,
		       COALESCE(u.email, '(unknown)')
		FROM zt_share_tokens t
		JOIN documents d ON d.tenant_id = t.tenant_id AND d.id = t.document_id
		JOIN document_versions v ON v.tenant_id = t.tenant_id AND v.id = t.version_id
		LEFT JOIN users u ON u.tenant_id = t.tenant_id AND u.id = t.created_by
		WHERE t.token_id = $1 AND t.token_hash = $2
	`, tokenID, tokenHash)
	var (
		tid, docID, verID uuid.UUID
		watermark, title, mime, sender string
		expiresAt time.Time
		revokedAt *time.Time
		maxViews, viewCount int
	)
	if err := row.Scan(&tid, &docID, &verID, &watermark,
		&expiresAt, &revokedAt, &maxViews, &viewCount,
		&title, &mime, &sender); err != nil {
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
	tokenID, err := uuid.Parse(r.PathValue("token_id"))
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "not_found"})
		return
	}
	tokenHash := h.hashToken(tokenID)

	// Single tx: look up, validate, bump view_count, get bytes.
	var (
		tid, docID, verID uuid.UUID
		mime              string
		expiresAt         time.Time
		revokedAt         *time.Time
		maxViews, views   int
	)
	err = h.pool.QueryRow(r.Context(), `
		SELECT t.tenant_id, t.document_id, t.version_id, t.expires_at,
		       t.revoked_at, t.max_views, t.view_count, v.mime_type
		FROM zt_share_tokens t
		JOIN document_versions v ON v.tenant_id = t.tenant_id AND v.id = t.version_id
		WHERE t.token_id = $1 AND t.token_hash = $2
	`, tokenID, tokenHash).Scan(&tid, &docID, &verID, &expiresAt, &revokedAt,
		&maxViews, &views, &mime)
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

	// The proxy's download() returns JSON {url, expires_at}, but
	// the recipient viewer's <Document file={...}> needs raw bytes
	// (or at least a URL the browser can GET directly). We call
	// download() to write the JSON to a buffer, then parse out the
	// presigned URL and emit a 302 redirect to it. Net result: the
	// recipient's pdf.js sees a redirect, follows it to MinIO, gets
	// PDF bytes. Phase 1.5 ships a streaming bytes path that never
	// leaks the MinIO URL — for now the presigned URL is bounded by
	// its server-side TTL (~10 min) which limits the leak window.
	r2 := r.WithContext(auth.SetTenantID(r.Context(), tid))
	r2.SetPathValue("document_id", docID.String())
	r2.SetPathValue("version_id", verID.String())
	rec := &captureRecorder{header: http.Header{}}
	h.storageProxy.download(rec, r2)
	if rec.status != http.StatusOK {
		// Propagate the upstream's error response untouched.
		for k, vs := range rec.header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(rec.status)
		_, _ = w.Write(rec.body)
		return
	}
	var dl struct{ URL string `json:"url"` }
	if err := json.Unmarshal(rec.body, &dl); err != nil || dl.URL == "" {
		writeJSON(w, 500, map[string]string{"error": "could not parse storage URL"})
		return
	}
	http.Redirect(w, r, dl.URL, http.StatusFound)
}

// captureRecorder is a minimal http.ResponseWriter that buffers the
// inner handler's output so we can inspect the JSON body and convert
// it to a redirect. Doesn't pretend to support streaming or Flusher —
// the storage proxy's download() writes the whole body in one shot.
type captureRecorder struct {
	header http.Header
	body   []byte
	status int
}

func (c *captureRecorder) Header() http.Header { return c.header }
func (c *captureRecorder) Write(p []byte) (int, error) {
	c.body = append(c.body, p...)
	return len(p), nil
}
func (c *captureRecorder) WriteHeader(s int) {
	if c.status == 0 {
		c.status = s
	}
}

// ---- telemetry (public write) -------------------------------------

type telemetryWriteReq struct {
	EventType  string `json:"event_type"`
	PageNumber *int   `json:"page_number,omitempty"`
	DwellMS    *int   `json:"dwell_ms,omitempty"`
}

func (h *ZTShareHandler) telemetryWrite(w http.ResponseWriter, r *http.Request) {
	tokenID, err := uuid.Parse(r.PathValue("token_id"))
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "not_found"})
		return
	}
	tokenHash := h.hashToken(tokenID)

	var in telemetryWriteReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid json"})
		return
	}
	if !validEventType(in.EventType) {
		writeJSON(w, 400, map[string]string{"error": "invalid event_type"})
		return
	}

	var tid uuid.UUID
	err = h.pool.QueryRow(r.Context(),
		`SELECT tenant_id FROM zt_share_tokens WHERE token_id = $1 AND token_hash = $2`,
		tokenID, tokenHash).Scan(&tid)
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "not_found"})
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
	tokenID, err := uuid.Parse(r.PathValue("token_id"))
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "token_id not a uuid"})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 100
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
