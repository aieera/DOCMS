// ADR 0037 — public, unauthenticated DSR intake.
//
//	POST /api/v1/dsr/intake          — subject-initiated request
//	GET  /api/v1/dsr/status?token=…  — public status read
//	POST /api/v1/dsr/status/verify   — magic-link redemption
//
// These three routes have NO session auth — they're the entry point
// before the subject has any credentials in our system. Mitigations
// (per ADR 0037 §"What we did not do"):
//
//  1. Gateway rate limit: 5 / IP / hour, 1 / email / hour. Config in
//     deploy/helm/gateway-routes.yaml.
//  2. No user-existence oracle: every well-formed request returns
//     202 Accepted with a generic "if a matching record exists you
//     will receive an email" body. ResolveSubject inside the
//     workflow handles "no such email" gracefully.
//  3. Constant-time token comparison via SHA-256 hash lookup. The
//     plaintext token leaves our system exactly once — in the
//     verification email.
//  4. Generic 404 from /status if the token doesn't match — never
//     leaks whether the request exists.

package handler

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/pkg/database"
)

// DSRPublicHandler mounts the unauthenticated DSR intake + status
// surface. It does NOT use the document service's session-auth
// middleware; tenant resolution happens via the tenant_slug in the
// request body / query string and the database GUC is set explicitly
// for the public-form code path.
type DSRPublicHandler struct {
	pool   *pgxpool.Pool
	outbox *database.OutboxRepository
	log    zerolog.Logger
}

// NewDSRPublicHandler constructs the handler.
func NewDSRPublicHandler(pool *pgxpool.Pool, outbox *database.OutboxRepository, log zerolog.Logger) *DSRPublicHandler {
	return &DSRPublicHandler{pool: pool, outbox: outbox, log: log}
}

// Register attaches the three public routes.
func (h *DSRPublicHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/dsr/intake", h.intake)
	mux.HandleFunc("GET /api/v1/dsr/status", h.status)
	mux.HandleFunc("POST /api/v1/dsr/status/verify", h.verify)
}

type intakeBody struct {
	TenantSlug     string `json:"tenant_slug"`
	RequesterEmail string `json:"requester_email"`
	// Public form maps the four GDPR articles to four request_type values.
	// Internally we map these onto the three existing privacy_dsr_requests
	// types (export | erase | anonymize) — rectification rides on the
	// export-then-edit flow per ADR 0024 (admin profile-edit UI).
	RequestType string `json:"request_type"` // access | rectification | erasure | portability
	Description string `json:"description"`
}

// statusOK is the generic accepted response. We deliberately don't echo
// back the email or the token — the email path is the only place those
// are surfaced.
type statusOK struct {
	Accepted bool   `json:"accepted"`
	Message  string `json:"message"`
	// Expires_at lets the public form show a "your link is valid for
	// 7 days" countdown without revealing any tenant-internal state.
	ExpiresAt time.Time `json:"expires_at"`
}

func (h *DSRPublicHandler) intake(w http.ResponseWriter, r *http.Request) {
	var body intakeBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeProxyJSON(w, http.StatusBadRequest, map[string]any{
			"type": "INVALID_ARGUMENT", "message": "invalid json",
		})
		return
	}
	body.TenantSlug = strings.ToLower(strings.TrimSpace(body.TenantSlug))
	body.RequesterEmail = strings.ToLower(strings.TrimSpace(body.RequesterEmail))
	body.RequestType = strings.ToLower(strings.TrimSpace(body.RequestType))

	if body.TenantSlug == "" || body.RequesterEmail == "" || body.RequestType == "" {
		writeProxyJSON(w, http.StatusBadRequest, map[string]any{
			"type": "INVALID_ARGUMENT", "message": "tenant_slug, requester_email, request_type required",
		})
		return
	}
	internalType, ok := mapPublicRequestType(body.RequestType)
	if !ok {
		writeProxyJSON(w, http.StatusBadRequest, map[string]any{
			"type":    "INVALID_ARGUMENT",
			"message": "request_type must be one of access | rectification | erasure | portability",
		})
		return
	}

	// Resolve the tenant — but DON'T leak the result. A bad slug returns
	// the same generic 202 a good slug + non-existent email would. This
	// is the user-existence oracle defense from ADR 0037.
	var tenantID uuid.UUID
	err := h.pool.QueryRow(r.Context(),
		`SELECT id FROM organizations WHERE slug = $1 AND deleted_at IS NULL`,
		body.TenantSlug,
	).Scan(&tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Tenant doesn't exist — return the same generic 202 a real
		// tenant would. Drops requests silently; legit subjects can
		// retry once they have the right slug.
		h.log.Info().Str("slug", body.TenantSlug).Msg("dsr intake: unknown tenant slug; silently 202")
		writeProxyJSON(w, http.StatusAccepted, statusOK{
			Accepted: true,
			Message:  "If your record exists in our system you will receive an email with verification instructions.",
			ExpiresAt: time.Now().UTC().Add(7 * 24 * time.Hour),
		})
		return
	}
	if err != nil {
		h.log.Error().Err(err).Msg("dsr intake: tenant lookup")
		writeProxyJSON(w, http.StatusInternalServerError, map[string]any{
			"type": "INTERNAL", "message": "internal error",
		})
		return
	}

	// Mint the token. 32 bytes of CSRNG → URL-safe hex. The plaintext
	// is only used to seed the email body and the response (we don't
	// echo it back); the SHA-256 lives on the row.
	plaintext, hash, err := mintStatusToken()
	if err != nil {
		h.log.Error().Err(err).Msg("dsr intake: mint token")
		writeProxyJSON(w, http.StatusInternalServerError, map[string]any{
			"type": "INTERNAL", "message": "internal error",
		})
		return
	}
	expiresAt := time.Now().UTC().Add(7 * 24 * time.Hour)

	// Insert + notify atomically. RLS context required; set inside the
	// tx because the public path never went through the session-auth
	// middleware that would set it for us.
	requestID := uuid.New()
	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		// requested_by is intentionally NULL for public-form requests —
		// no internal user authored this; only requester_email identifies
		// the subject. The schema allows NULL on requested_by.
		if _, err := tx.Exec(r.Context(), `
			INSERT INTO privacy_dsr_requests (
				id, tenant_id, request_type, subject_email, requested_by,
				status, status_token_hash, intake_source, result_summary,
				created_at, due_at
			) VALUES (
				$1, $2, $3, $4, NULL,
				'pending', $5, 'public_form', $6,
				now(), now() + interval '30 days'
			)`,
			requestID, tenantID, internalType, body.RequesterEmail,
			hash,
			mustJSON(map[string]any{
				"intake_description": body.Description,
				"public_request_type": body.RequestType,
			}),
		); err != nil {
			return fmt.Errorf("insert dsr row: %w", err)
		}
		// Emit dms.notify.dsr_intake.v1. The notification service's
		// dms.notify.> consumer routes this to the email path. Token
		// goes in the payload exactly once — never logged, never
		// returned to the caller.
		payload := map[string]any{
			"tenant_id":     tenantID.String(),
			"user_ids":      []string{}, // public form: no user; routes via email field
			"recipient_email": body.RequesterEmail,
			"type":          "dsr.intake.verify",
			"title":         "Verify your data request",
			"body": fmt.Sprintf(
				"You (or someone using your email) submitted a %s request. Click the link below within 7 days to verify:\n\nhttps://%s/dsr-status?token=%s\n\nIf you didn't request this, ignore this email.",
				body.RequestType, body.TenantSlug, plaintext,
			),
			"resource_type": "privacy_dsr_request",
			"resource_id":   requestID.String(),
		}
		body, _ := json.Marshal(payload)
		evt := database.NewOutboxEvent(tenantID, "dms.notify.dsr_intake.v1", "privacy_dsr_request", requestID, body)
		return h.outbox.Insert(r.Context(), tx, evt)
	})
	if err != nil {
		h.log.Error().Err(err).Msg("dsr intake: persist")
		writeProxyJSON(w, http.StatusInternalServerError, map[string]any{
			"type": "INTERNAL", "message": "internal error",
		})
		return
	}

	h.log.Info().
		Str("tenant", tenantID.String()).
		Str("request", requestID.String()).
		Str("intake_source", "public_form").
		Str("type", body.RequestType).
		Msg("dsr public intake accepted")
	writeProxyJSON(w, http.StatusAccepted, statusOK{
		Accepted:  true,
		Message:   "If your record exists in our system you will receive an email with verification instructions.",
		ExpiresAt: expiresAt,
	})
}

// status looks up a request by its public token. Returns a sparse
// view: status, type, due_at, and (if completed) the artifact URL.
// Never leaks tenant id / requester id / blocked_reason internals.
func (h *DSRPublicHandler) status(w http.ResponseWriter, r *http.Request) {
	plaintext := r.URL.Query().Get("token")
	if plaintext == "" {
		writeProxyJSON(w, http.StatusBadRequest, map[string]any{
			"type": "INVALID_ARGUMENT", "message": "token required",
		})
		return
	}
	hash := hashToken(plaintext)
	// No tenant scope — the hash is globally unique by design (32 bytes
	// of entropy). The lookup uses the partial index
	// idx_dsr_status_token (tenant_id, status_token_hash) which works
	// across tenants for an admin-bypass tenant context. We deliberately
	// SET row_security = off here because the public caller has no
	// tenant identity yet; the token is the auth token.
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		writeProxyJSON(w, http.StatusInternalServerError, map[string]any{"type": "INTERNAL", "message": "internal error"})
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), "SET LOCAL row_security = off"); err != nil {
		writeProxyJSON(w, http.StatusInternalServerError, map[string]any{"type": "INTERNAL", "message": "internal error"})
		return
	}

	var (
		requestType, statusStr string
		createdAt, dueAt       time.Time
		completedAt            *time.Time
		verifiedAt             *time.Time
	)
	err = tx.QueryRow(r.Context(), `
		SELECT request_type, status, created_at, due_at,
		       completed_at, requester_identity_verified_at
		  FROM privacy_dsr_requests
		 WHERE status_token_hash = $1`,
		hash,
	).Scan(&requestType, &statusStr, &createdAt, &dueAt, &completedAt, &verifiedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProxyJSON(w, http.StatusNotFound, map[string]any{"type": "NOT_FOUND", "message": "request not found"})
		return
	}
	if err != nil {
		h.log.Error().Err(err).Msg("dsr status lookup")
		writeProxyJSON(w, http.StatusInternalServerError, map[string]any{"type": "INTERNAL", "message": "internal error"})
		return
	}

	out := map[string]any{
		"request_type":   requestType,
		"status":         statusStr,
		"created_at":     createdAt,
		"due_at":         dueAt,
		"verified":       verifiedAt != nil,
	}
	if completedAt != nil {
		out["completed_at"] = completedAt
	}
	writeProxyJSON(w, http.StatusOK, out)
}

// verify stamps requester_identity_verified_at. Required before the
// erasure / rectification mutate workflows are willing to proceed
// (read-only access / portability can run without this gate).
func (h *DSRPublicHandler) verify(w http.ResponseWriter, r *http.Request) {
	var body struct{ Token string `json:"token"` }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Token == "" {
		writeProxyJSON(w, http.StatusBadRequest, map[string]any{"type": "INVALID_ARGUMENT", "message": "token required"})
		return
	}
	hash := hashToken(body.Token)
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		writeProxyJSON(w, http.StatusInternalServerError, map[string]any{"type": "INTERNAL", "message": "internal error"})
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), "SET LOCAL row_security = off"); err != nil {
		writeProxyJSON(w, http.StatusInternalServerError, map[string]any{"type": "INTERNAL", "message": "internal error"})
		return
	}
	tag, err := tx.Exec(r.Context(), `
		UPDATE privacy_dsr_requests
		   SET requester_identity_verified_at = COALESCE(requester_identity_verified_at, now())
		 WHERE status_token_hash = $1`,
		hash,
	)
	if err != nil {
		h.log.Error().Err(err).Msg("dsr verify update")
		writeProxyJSON(w, http.StatusInternalServerError, map[string]any{"type": "INTERNAL", "message": "internal error"})
		return
	}
	if tag.RowsAffected() == 0 {
		writeProxyJSON(w, http.StatusNotFound, map[string]any{"type": "NOT_FOUND", "message": "request not found"})
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeProxyJSON(w, http.StatusInternalServerError, map[string]any{"type": "INTERNAL", "message": "internal error"})
		return
	}
	writeProxyJSON(w, http.StatusOK, map[string]any{"verified": true})
}

// mintStatusToken returns (plaintext, sha256_hash, error). The plaintext
// is hex-encoded so it's URL-safe without further escaping.
func mintStatusToken() (string, []byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	plaintext := hex.EncodeToString(raw)
	h := sha256.Sum256([]byte(plaintext))
	return plaintext, h[:], nil
}

func hashToken(plaintext string) []byte {
	h := sha256.Sum256([]byte(plaintext))
	return h[:]
}

// mapPublicRequestType maps the four GDPR public values onto the three
// existing privacy_dsr_requests internal types. Rectification rides
// on export per ADR 0024 (the subject downloads their data, files a
// rectification request which an admin actions via the profile-edit
// UI, then a follow-up `dms.user.updated` event fans out across
// services).
func mapPublicRequestType(public string) (string, bool) {
	switch public {
	case "access", "portability":
		return "export", true
	case "erasure":
		return "erase", true
	case "rectification":
		return "export", true // see comment above
	}
	return "", false
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
