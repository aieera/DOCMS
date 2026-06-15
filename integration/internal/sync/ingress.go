package sync

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/integration/internal/erp"
	"github.com/aieera/sedoc/integration/internal/store"
)

// Ingress is the worker's HTTP surface: the ERP webhook receiver plus a few
// read/retry endpoints the Sync Dashboard ([F]) and tests use. It NEVER calls
// SeDoc inline — the webhook just records the event and returns fast; the worker
// loop does the SeDoc work asynchronously.
type Ingress struct {
	st  *store.Store
	log zerolog.Logger
}

// NewIngress constructs the HTTP surface.
func NewIngress(st *store.Store, log zerolog.Logger) *Ingress {
	return &Ingress{st: st, log: log}
}

// Register mounts routes.
func (i *Ingress) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /webhooks/erp", i.webhook)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /sync/log", i.listLog)
	mux.HandleFunc("POST /sync/retry/{id}", i.retry)
}

func (i *Ingress) webhook(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "read body"})
		return
	}
	var e erp.Event
	if err := json.Unmarshal(raw, &e); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
		return
	}
	if verr := e.Validate(); verr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": verr.Error()})
		return
	}
	id, isNew, err := i.st.InsertEvent(r.Context(), e.ERPEventID, e.Kind, e.CustomerRef, raw)
	if err != nil {
		i.log.Error().Err(err).Str("erp_event_id", e.ERPEventID).Msg("record event")
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "record event"})
		return
	}
	// 202 either way — a duplicate delivery is success (already queued/done).
	writeJSON(w, http.StatusAccepted, map[string]any{"sync_id": id.String(), "duplicate": !isNew})
}

func (i *Ingress) listLog(w http.ResponseWriter, r *http.Request) {
	rows, err := i.st.ListSyncLog(r.Context(), r.URL.Query().Get("status"), 100)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows})
}

func (i *Ingress) retry(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad id"})
		return
	}
	if err := i.st.RetryFailed(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
