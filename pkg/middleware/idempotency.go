// Package middleware: Idempotency makes unsafe requests carrying an
// `Idempotency-Key` header replay-safe. The first successful execution per
// (tenant, key) is stored; any later request with the same key replays that
// exact response instead of re-running the handler. This gives the ERP
// outbound push the same "one document per record, retries never duplicate"
// guarantee its dms_sync_log enforces via UNIQUE(entity_type, entity_id).
//
// Concurrency: the key is RESERVED with an atomic INSERT before the handler
// runs, so two simultaneous retries can't both create. The loser sees the
// reservation and gets 409 (in-flight) — a transient the BullMQ worker retries.
//
// Requests without the header pass through untouched, so this is opt-in and
// never changes behaviour for the web UI.
//
// Wrap AFTER auth + tenant middleware (it needs the tenant on ctx) and BEFORE
// the business handler.
package middleware

import (
	"bytes"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vaultdms/vaultdms/pkg/auth"
	"github.com/vaultdms/vaultdms/pkg/database"
)

const (
	idempotencyHeader        = "Idempotency-Key"
	idempotencyReplayedHeader = "Idempotency-Replayed"
	// Cap on cached response size. Larger successful responses still return
	// to the caller; they just aren't stored (so a replay re-executes).
	maxStoredBodyBytes = 1 << 20 // 1 MiB
)

// Idempotency returns middleware backed by the idempotency_keys table.
func Idempotency(pool *pgxpool.Pool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get(idempotencyHeader)
			if key == "" || isSafeMethod(r.Method) {
				next.ServeHTTP(w, r)
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
					SELECT status, COALESCE(response_status, 0), response_body
					FROM idempotency_keys
					WHERE tenant_id = $1 AND idempotency_key = $2`,
					tenantID, key).Scan(&existState, &existStatus, &existBody)
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
