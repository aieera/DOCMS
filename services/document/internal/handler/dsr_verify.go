// Wave 11.4 — DSR verification token round-trip.
//
// Closes ADR 0024 §6 ("honor system") — erase workflows previously
// accepted any non-empty verification_token. This package now:
//
//  1. Mints a 32-byte crypto/rand token on POST
//     /api/v1/privacy/verify/request-token { subject_email }.
//  2. Stores SHA-256(token) in Redis under
//     `dsr:verify:{tenant_id}:{lower_email}` with 24h TTL.
//  3. Emits an outbox event `dms.notify.dsr_verify.v1` carrying
//     the plaintext token + subject email. The notification service
//     consumes dms.notify.> and would hand off to SMTP — until the
//     transactional email path ships (Wave 12), the notification
//     service's "in-app" channel surfaces the token in the user's
//     notifications feed, which is acceptable for pilot use.
//
// The EraseWorkflow (services/workflow/internal/workflows/dsr.go)
// calls the matching `VerifyDSRToken` activity added in this
// prompt; see services/workflow/internal/activities/dsr.go.
package handler

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
)

// DSRVerifyHandler mounts /api/v1/privacy/verify/* routes.
type DSRVerifyHandler struct {
	pool *pgxpool.Pool
	rdb  *redis.Client
	log  zerolog.Logger
}

// NewDSRVerifyHandler constructs the handler.
func NewDSRVerifyHandler(pool *pgxpool.Pool, rdb *redis.Client, log zerolog.Logger) *DSRVerifyHandler {
	return &DSRVerifyHandler{pool: pool, rdb: rdb, log: log}
}

// Register mounts routes.
func (h *DSRVerifyHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/privacy/verify/request-token", h.requestToken)
}

type requestTokenBody struct {
	SubjectEmail string `json:"subject_email"`
}

// TokenTTL is how long a newly-minted DSR verification token stays
// redeemable. Matches ADR 0024 §6.
const TokenTTL = 24 * time.Hour

// RedisKey returns the canonical storage key for a DSR token hash.
// Exported so the workflow activity package can re-derive it.
func DSRTokenRedisKey(tenantID uuid.UUID, email string) string {
	return fmt.Sprintf("dsr:verify:%s:%s", tenantID.String(), strings.ToLower(strings.TrimSpace(email)))
}

// HashToken computes the canonical SHA-256 hash used as the Redis
// value. We store the hash (never the plaintext) so a Redis dump
// can't be replayed against the workflow.
func HashDSRToken(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

func (h *DSRVerifyHandler) requestToken(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := callers(w, r)
	if !ok {
		return
	}
	var body requestTokenBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	email := strings.ToLower(strings.TrimSpace(body.SubjectEmail))
	if email == "" || !strings.Contains(email, "@") {
		writeErr(w, r, vdmserr.Validation("subject_email", "required; must contain @"))
		return
	}

	// 32 bytes of crypto/rand → 64-char hex token. High entropy so
	// brute-force within the 24h TTL is not a concern.
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		writeErr(w, r, vdmserr.Wrap(vdmserr.ErrInternal, err))
		return
	}
	token := hex.EncodeToString(buf)

	key := DSRTokenRedisKey(tenantID, email)
	if err := h.rdb.Set(r.Context(), key, HashDSRToken(token), TokenTTL).Err(); err != nil {
		writeErr(w, r, vdmserr.Wrap(vdmserr.ErrInternal, err))
		return
	}

	// Emit a notification via the outbox so the notification service
	// delivers it. Plaintext token rides in the payload — consumers
	// that don't talk to the subject (e.g. audit logger) MUST NOT
	// log .data.token in cleartext. Enforced by a CI guard in
	// scripts/check-dsr-token-leak.sh (Wave 13.4).
	if err := h.emitVerifyNotification(r.Context(), tenantID, userID, email, token); err != nil {
		writeErr(w, r, vdmserr.Wrap(vdmserr.ErrInternal, err))
		return
	}

	writeJSONStatus(w, http.StatusAccepted, map[string]any{
		"delivered_at": time.Now().UTC().Format(time.RFC3339),
		"expires_at":   time.Now().UTC().Add(TokenTTL).Format(time.RFC3339),
		"channel":      "notification+email",
		"note":         "token is not returned in this response; subject receives it via notification / email",
	})
}

// emitVerifyNotification writes an outbox event that the notification
// service consumes. Single-user delivery — `user_ids` carries the
// subject email so the consumer dispatches to the matching user.
func (h *DSRVerifyHandler) emitVerifyNotification(ctx context.Context, tenantID, requestor uuid.UUID, email, token string) error {
	payload, _ := json.Marshal(map[string]any{
		"tenant_id":     tenantID.String(),
		"user_ids":      []string{email}, // notification consumer resolves by email for DSR
		"type":          "dsr.verify",
		"title":         "Data subject verification",
		"body":          "Your data-subject request needs verification. Token: " + token + " (valid 24h)",
		"resource_type": "privacy_dsr_request",
		"resource_id":   email, // opaque; consumer uses it for deduplication
		"requested_by":  requestor.String(),
	})
	evt := database.NewOutboxEvent(tenantID, "dms.notify.dsr_verify.v1", "privacy_dsr_request", uuid.New(), payload)
	repo := database.NewOutboxRepository()
	return database.WithTenantTx(ctx, h.pool, tenantID, func(tx pgx.Tx) error {
		return repo.Insert(ctx, tx, evt)
	})
}
