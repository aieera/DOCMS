// ADR 0036 — POST /internal/v1/disposition/execute. Cron-driven; runs
// hourly per tenant via the vaultdms-disposition CronJob. Same auth
// + path conventions as /internal/v1/retention/sweep — internal-only,
// shared-secret in Authorization header.

package handler

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/document/internal/service"
)

// DispositionExecutorHandler exposes the executor cron entry point.
type DispositionExecutorHandler struct {
	svc *service.DocumentService
	log zerolog.Logger
}

// NewDispositionExecutorHandler constructs the handler.
func NewDispositionExecutorHandler(svc *service.DocumentService, log zerolog.Logger) *DispositionExecutorHandler {
	return &DispositionExecutorHandler{svc: svc, log: log}
}

// Register attaches the route.
func (h *DispositionExecutorHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /internal/v1/disposition/execute", h.execute)
}

type executeRequest struct {
	TenantID  string `json:"tenant_id"`
	BatchSize int    `json:"batch_size"`
}

func (h *DispositionExecutorHandler) execute(w http.ResponseWriter, r *http.Request) {
	if !checkInternalKey(r) {
		writeErr(w, r, vdmserr.ErrUnauthorized)
		return
	}
	var body executeRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	tenantID, err := uuid.Parse(body.TenantID)
	if err != nil {
		writeErr(w, r, vdmserr.Validation("tenant_id", "invalid uuid"))
		return
	}
	res, err := h.svc.ExecuteDispositions(r.Context(), tenantID, body.BatchSize)
	if err != nil {
		h.log.Error().Err(err).Str("tenant", tenantID.String()).Msg("disposition execute")
		writeErr(w, r, err)
		return
	}
	h.log.Info().
		Str("tenant", tenantID.String()).
		Int("inspected", res.Inspected).
		Int("executed", res.Executed).
		Int("skipped_not_ready", res.SkippedNotReady).
		Int("superseded_by_hold", res.SupersededByHold).
		Int("errors", res.Errors).
		Msg("disposition execute complete")
	writeJSONStatus(w, http.StatusOK, res)
}
