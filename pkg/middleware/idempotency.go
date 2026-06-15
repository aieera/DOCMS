// Package middleware: Idempotency makes unsafe requests carrying an
// `Idempotency-Key` header replay-safe. The first successful execution per
// (tenant, key) is stored; any later request with the same key replays that
// exact response instead of re-running the handler. This gives the ERP
// outbound push the same "one document per record, retries never duplicate"
// guarantee its dms_sync_log enforces via UNIQUE(entity_type, entity_id).
//
// Contract (status semantics):
//   - first request          → handler runs; success (2xx, ≤1 MiB body) is
//     stored; response returned with Idempotency-Replayed: false.
//   - replay (same key)      → stored response replayed verbatim, Idempotency-Replayed: true.
//   - concurrent in-flight   → 409 IDEMPOTENCY_IN_PROGRESS (the reservation row
//     exists but isn't completed yet; caller retries).
//   - key reused for a       → 422 IDEMPOTENCY_KEY_REUSED (method/path differ
//     different request          from the stored request).
//   - store unavailable      → 503 IDEMPOTENCY_STORE_UNAVAILABLE (fail-closed:
//     never pass through, which would risk a duplicate).
//   - key > 200 chars        → 400 IDEMPOTENCY_KEY_TOO_LONG.
//   - missing key (required) → 400 IDEMPOTENCY_KEY_REQUIRED — only under
//     IdempotencyRequired AND only for API-key callers.
//
// Concurrency: the key is RESERVED with an atomic INSERT before the handler
// runs, so two simultaneous retries can't both create. The loser sees the
// reservation and gets 409 (in-flight) — a transient the BullMQ worker retries.
//
// Two entry points:
//   - Idempotency          — opt-in: requests without the header pass through
//     untouched, so the web UI (session) is unaffected.
//   - IdempotencyRequired  — additionally rejects an unsafe request from an
//     API-key principal that omits the header (400). The
//     web UI is still exempt (only api_key-role callers
//     are forced), so the ERP integration must send a key
//     on resource-creating POSTs while the browser flow
//     is unchanged.
//
// Wrap AFTER auth + tenant middleware (it needs the tenant + role on ctx) and
// BEFORE the business handler.
package middleware

import (
	"bytes"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
)

const (
	idempotencyHeader         = "Idempotency-Key"
	idempotencyReplayedHeader = "Idempotency-Replayed"
	// Cap on cached response size. Larger successful responses still return
	// to the caller; they just aren't stored (so a replay re-executes).
	maxStoredBodyBytes = 1 << 20 // 1 MiB
	// Cap on the client-supplied key length. Bounds storage/abuse from a
	// caller (authenticated, but still) sending pathologically long keys.
	maxKeyLen = 200
	// apiKeyRole is the role APIKeyAuth stamps on the identity (see apikey.go).
	// IdempotencyRequired only forces the header for these principals so the
	// session-authed web UI keeps working without one.
	apiKeyRole = "api_key"
)

// Idempotency returns opt-in middleware backed by the idempotency_keys table:
// requests without the header pass through untouched.
func Idempotency(pool *pgxpool.Pool) func(http.Handler) http.Handler {
	return idempotencyMW(pool, false)
}

// IdempotencyRequired is Idempotency plus a hard requirement: an unsafe request
// from an API-key principal MUST carry the header (else 400). Session callers
// stay exempt. Use on resource-creating POSTs the ERP integration drives, so a
// re-fired webhook can't spawn a duplicate.
func IdempotencyRequired(pool *pgxpool.Pool) func(http.Handler) http.Handler {
	return idempotencyMW(pool, true)
}

func idempotencyMW(pool *pgxpool.Pool, required bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isSafeMethod(r.Method) {
				next.ServeHTTP(w, r)
				return
			}
			key := r.Header.Get(idempotencyHeader)
			if key == "" {
				// Force the header for the integration (API-key callers) on
				// required routes; the web UI (session) is left opt-in.
				if required && auth.GetUserRole(r.Context()) == apiKeyRole {
					writeJSON(w, http.StatusBadRequest, map[string]any{
						"type":           "IDEMPOTENCY_KEY_REQUIRED",
						"message":        "Idempotency-Key header is required on this request",
						"correlation_id": auth.GetCorrelationID(r.Context()),
					})
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			if len(key) > maxKeyLen {
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"type":           "IDEMPOTENCY_KEY_TOO_LONG",
					"message":        "Idempotency-Key must be at most 200 characters",
					"correlation_id": auth.GetCorrelationID(r.Context()),
				})
				return
			}
			tenantID, err := auth.GetTenantID(r.Context())
			if err != nil || tenantID == uuid.Nil {
				// No tenant on ctx → can't scope the key. Let the handler
				// (and its own auth) handle the request.
				next.ServeHTTP(w, r)
				return
			}

			// 1. Reserve. RowsAffected==1 → we own this execution; 0 → a row
			//    already exists (completed replay, or another in-flight retry).
			var (
				owned       bool
				existState  string
				existStatus int
				existBody   []byte
				existMethod string
				existPath   string
			)
			rerr := database.WithTenantTx(r.Context(), pool, tenantID, func(tx pgx.Tx) error {
				tag, e := tx.Exec(r.Context(), `
					INSERT INTO idempotency_keys (tenant_id, idempotency_key, request_method, request_path, status)
					VALUES ($1, $2, $3, $4, 'in_progress')
					ON CONFLICT (tenant_id, idempotency_key) DO NOTHING`,
					tenantID, key, r.Method, r.URL.Path)
				if e != nil {
					return e
				}
				if tag.RowsAffected() == 1 {
					owned = true
					return nil
				}
				return tx.QueryRow(r.Context(), `
					SELECT status, COALESCE(response_status, 0), response_body, request_method, request_path
					FROM idempotency_keys
					WHERE tenant_id = $1 AND idempotency_key = $2`,
					tenantID, key).Scan(&existState, &existStatus, &existBody, &existMethod, &existPath)
			})
			if rerr != nil {
				// Fail safe: do NOT pass through (that risks a duplicate write).
				// 503 is transient — the worker retries.
				writeJSON(w, http.StatusServiceUnavailable, map[string]any{
					"type":           "IDEMPOTENCY_STORE_UNAVAILABLE",
					"message":        "idempotency store unavailable",
					"correlation_id": auth.GetCorrelationID(r.Context()),
				})
				return
			}

			// 2. Not owned — replay or in-flight.
			if !owned {
				// Reusing one key for a different request must NOT replay the
				// original response — that would be silently wrong. The stored
				// method/path identify the original request; mismatch → 422.
				if existMethod != r.Method || existPath != r.URL.Path {
					writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
						"type":           "IDEMPOTENCY_KEY_REUSED",
						"message":        "Idempotency-Key was already used for a different request",
						"correlation_id": auth.GetCorrelationID(r.Context()),
					})
					return
				}
				if existState == "completed" {
					w.Header().Set(idempotencyReplayedHeader, "true")
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(existStatus)
					_, _ = w.Write(existBody)
					return
				}
				// in_progress: another execution holds the key.
				writeJSON(w, http.StatusConflict, map[string]any{
					"type":           "IDEMPOTENCY_IN_PROGRESS",
					"message":        "a request with this Idempotency-Key is in progress; retry shortly",
					"correlation_id": auth.GetCorrelationID(r.Context()),
				})
				return
			}

			// 3. We own it — execute into a buffer so we can persist before flushing.
			cap := &captureWriter{header: http.Header{}, status: http.StatusOK, buf: &bytes.Buffer{}}
			next.ServeHTTP(cap, r)

			success := cap.status >= 200 && cap.status < 300
			if success && cap.buf.Len() <= maxStoredBodyBytes {
				_ = database.WithTenantTx(r.Context(), pool, tenantID, func(tx pgx.Tx) error {
					_, e := tx.Exec(r.Context(), `
						UPDATE idempotency_keys
						SET status = 'completed', response_status = $3, response_body = $4, completed_at = now()
						WHERE tenant_id = $1 AND idempotency_key = $2`,
						tenantID, key, cap.status, cap.buf.Bytes())
					return e
				})
			} else {
				// Failure (or un-cacheable body): release the reservation so a
				// later retry can re-execute instead of being stuck.
				_ = database.WithTenantTx(r.Context(), pool, tenantID, func(tx pgx.Tx) error {
					_, e := tx.Exec(r.Context(), `
						DELETE FROM idempotency_keys
						WHERE tenant_id = $1 AND idempotency_key = $2 AND status = 'in_progress'`,
						tenantID, key)
					return e
				})
			}

			// 4. Flush the captured response to the real client.
			for k, vs := range cap.header {
				for _, v := range vs {
					w.Header().Add(k, v)
				}
			}
			w.Header().Set(idempotencyReplayedHeader, "false")
			w.WriteHeader(cap.status)
			_, _ = w.Write(cap.buf.Bytes())
		})
	}
}

// captureWriter buffers an http response (status + headers + body) so the
// idempotency layer can persist it before forwarding to the client.
type captureWriter struct {
	header http.Header
	status int
	buf    *bytes.Buffer
	wrote  bool
}

func (c *captureWriter) Header() http.Header { return c.header }

func (c *captureWriter) WriteHeader(status int) {
	if !c.wrote {
		c.status = status
		c.wrote = true
	}
}

func (c *captureWriter) Write(b []byte) (int, error) {
	if !c.wrote {
		c.WriteHeader(http.StatusOK)
	}
	return c.buf.Write(b)
}
