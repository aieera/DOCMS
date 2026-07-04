// Document WORM object-lock admin API (paired with services/storage ApplyWORM).
//
//	POST /api/v1/documents/{id}/worm-lock   {retain_until, mode}   (admin)
//	GET  /api/v1/documents/{id}/worm                                (status)
//
// worm-lock resolves the document's current blob, asks the storage service to
// apply an S3 object-lock retention (internal call), then records
// worm_retain_until on the document so the write-path guard (blockedByWORM)
// and the UI WORM indicator can read it.
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
)

type WORMHandler struct {
	pool       *pgxpool.Pool
	log        zerolog.Logger
	storageURL string // storage internal base, e.g. http://storage:8080
	internKey  string // SEDOC_INTERNAL_API_KEY
	hc         *http.Client
}

func NewWORMHandler(pool *pgxpool.Pool, log zerolog.Logger) *WORMHandler {
	url := os.Getenv("SEDOC_STORAGE_INTERNAL_URL")
	if url == "" {
		url = "http://storage:8080"
	}
	return &WORMHandler{
		pool: pool, log: log, storageURL: url,
		internKey: os.Getenv("SEDOC_INTERNAL_API_KEY"),
		hc:        &http.Client{Timeout: 30 * time.Second},
	}
}

func (h *WORMHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/documents/{id}/worm-lock", h.lock)
	mux.HandleFunc("GET /api/v1/documents/{id}/worm", h.status)
}

func (h *WORMHandler) status(w http.ResponseWriter, r *http.Request) {
	tenantID, err := auth.GetTenantID(r.Context())
	if err != nil || tenantID == uuid.Nil {
		writeErr(w, r, vdmserr.ErrUnauthorized)
		return
	}
	docID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	var until *time.Time
	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(),
			`SELECT worm_retain_until FROM documents WHERE tenant_id=$1 AND id=$2`,
			tenantID, docID).Scan(&until)
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	locked := until != nil && until.After(time.Now())
	writeJSONStatus(w, http.StatusOK, map[string]any{"worm_retain_until": until, "locked": locked})
}

func (h *WORMHandler) lock(w http.ResponseWriter, r *http.Request) {
	tenantID, err := auth.GetTenantID(r.Context())
	if err != nil || tenantID == uuid.Nil {
		writeErr(w, r, vdmserr.ErrUnauthorized)
		return
	}
	if !requireRole(w, r, "owner", "admin", "compliance_officer") {
		return
	}
	docID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	var body struct {
		RetainUntil string `json:"retain_until"` // RFC3339
		Mode        string `json:"mode"`         // GOVERNANCE | COMPLIANCE
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	retainUntil, perr := time.Parse(time.RFC3339, body.RetainUntil)
	if perr != nil || !retainUntil.After(time.Now()) {
		writeErr(w, r, vdmserr.Validation("retain_until", "must be a future RFC3339 time"))
		return
	}

	// Resolve the document's current (latest) version blob.
	var blobID string
	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `
			SELECT content_blob_id::text FROM document_versions
			 WHERE tenant_id=$1 AND document_id=$2
			 ORDER BY version_number DESC LIMIT 1`,
			tenantID, docID).Scan(&blobID)
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}

	// Ask storage to apply the S3 object-lock retention.
	if err := h.callStorageWORM(r.Context(), tenantID.String(), blobID, retainUntil, body.Mode); err != nil {
		h.log.Error().Err(err).Str("document", docID.String()).Msg("worm: storage apply failed")
		writeErr(w, r, vdmserr.Internal("storage object-lock failed: "+err.Error()))
		return
	}

	// Record the retention on the document for the guard + UI.
	if err := database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		_, e := tx.Exec(r.Context(),
			`UPDATE documents SET worm_retain_until=$3, updated_at=now() WHERE tenant_id=$1 AND id=$2`,
			tenantID, docID, retainUntil)
		return e
	}); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"status": "locked", "worm_retain_until": retainUntil})
}

func (h *WORMHandler) callStorageWORM(ctx context.Context, tenantID, blobID string, retainUntil time.Time, mode string) error {
	payload, _ := json.Marshal(map[string]any{
		"tenant_id":    tenantID,
		"blob_id":      blobID,
		"retain_until": retainUntil.UTC().Format(time.RFC3339),
		"mode":         mode,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.storageURL+"/internal/v1/worm-lock", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Service-Key", h.internKey)
	resp, err := h.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(resp.Body)
		return vdmserr.Internal("storage returned " + resp.Status + ": " + buf.String())
	}
	return nil
}
