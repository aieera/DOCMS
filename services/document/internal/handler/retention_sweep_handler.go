// §9.4 / G5 — POST /internal/v1/retention/sweep — called daily by
// the vaultdms-retention CronJob. Not publicly routable; path
// prefix is /internal/ and the gateway's routes.yaml omits it.
//
// Auth: SEDOC_INTERNAL_API_KEY in Authorization header. The
// CronJob template reads the same key from a Kubernetes Secret.
package handler

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/service"
)

type RetentionSweepHandler struct {
	svc *service.DocumentService
	log zerolog.Logger
}

func NewRetentionSweepHandler(svc *service.DocumentService, log zerolog.Logger) *RetentionSweepHandler {
	return &RetentionSweepHandler{svc: svc, log: log}
}

func (h *RetentionSweepHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /internal/v1/retention/sweep", h.sweep)
}

type sweepRequest struct {
	TenantID  string `json:"tenant_id"`
	BatchSize int    `json:"batch_size"`
}

func (h *RetentionSweepHandler) sweep(w http.ResponseWriter, r *http.Request) {
	if !checkInternalKey(r) {
		writeErr(w, r, vdmserr.ErrUnauthorized)
		return
	}
	var body sweepRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	tenantID, err := uuid.Parse(body.TenantID)
	if err != nil {
		writeErr(w, r, vdmserr.Validation("tenant_id", "invalid uuid"))
		return
	}
	result, err := h.svc.SweepRetention(r.Context(), tenantID, body.BatchSize)
	if err != nil {
		h.log.Error().Err(err).Str("tenant", tenantID.String()).Msg("retention sweep")
		writeErr(w, r, err)
		return
	}
	h.log.Info().
		Str("tenant", tenantID.String()).
		Int("policies_applied", result.PoliciesApplied).
		Int("archived", result.Archived).
		Int("disposed", result.Disposed).
		Int("skipped_by_hold", result.SkippedByHold).
		Int("errors", result.Errors).
		Msg("retention sweep complete")
	writeJSONStatus(w, http.StatusOK, result)
}

// checkInternalKey is a shared-secret check. The same secret is
// wired into the CronJob template's Secret env var. Returning 401
// keeps the endpoint from leaking its existence to unauthenticated
// callers (path IS unguessable anyway — /internal/ isn't in the
// gateway's allowlist).
func checkInternalKey(r *http.Request) bool {
	want := os.Getenv("SEDOC_INTERNAL_API_KEY")
	if want == "" {
		return false
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return got != "" && got == want
}
