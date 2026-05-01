// OCR-results read API + re-OCR trigger (paired with services/intelligence
// which owns the actual OCR work). The intelligence service writes
// rows into ocr_results via its Celery worker after consuming
// dms.version.uploaded.v1 — this handler is the read surface for the
// frontend's "Extracted text" tab.
//
// Routes (mounted in cmd/server/main.go on a SessionAuth-wrapped mux):
//
//   GET  /api/v1/documents/{id}/versions/{vid}/ocr
//   POST /api/v1/documents/{id}/versions/{vid}/ocr/rerun
//
// The first returns the persisted OCR rows; the second re-emits the
// trigger event so the intelligence worker re-processes.

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/pkg/auth"
	"github.com/vaultdms/vaultdms/pkg/database"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
)

type OCRHandler struct {
	pool *pgxpool.Pool
	log  zerolog.Logger
}

func NewOCRHandler(pool *pgxpool.Pool, log zerolog.Logger) *OCRHandler {
	return &OCRHandler{pool: pool, log: log}
}

func (h *OCRHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/documents/{id}/versions/{vid}/ocr", h.list)
	mux.HandleFunc("POST /api/v1/documents/{id}/versions/{vid}/ocr/rerun", h.rerun)
}

// OCRPage is the wire shape returned to the frontend. Mirrors the
// ocr_results columns plus a denormalized engine string so the UI
// can show "Surya · 96.2%" without joining anything else.
type OCRPage struct {
	ID               string  `json:"id"`
	VersionID        string  `json:"version_id"`
	PageNumber       int     `json:"page_number"`
	TextContent      string  `json:"text_content"`
	Confidence       float32 `json:"confidence"`
	Language         string  `json:"language,omitempty"`
	BoundingBoxes    json.RawMessage `json:"bounding_boxes"`
	ProcessingTimeMS *int    `json:"processing_time_ms,omitempty"`
	Engine           string  `json:"engine,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

type ocrListResponse struct {
	Pages       []OCRPage `json:"pages"`
	Status      string    `json:"status"` // pending | running | completed | failed | unknown
	TotalPages  int       `json:"total_pages"`
	AvgConfidence float32 `json:"avg_confidence"`
}

func (h *OCRHandler) list(w http.ResponseWriter, r *http.Request) {
	tenantID, err := auth.GetTenantID(r.Context())
	if err != nil || tenantID == uuid.Nil {
		writeErr(w, r, vdmserr.ErrUnauthorized)
		return
	}
	versionID, err := uuid.Parse(r.PathValue("vid"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("version_id", "not a uuid"))
		return
	}
	pages, status, err := h.queryOCR(r.Context(), tenantID, versionID)
	if err != nil {
		h.log.Error().Err(err).Str("version", versionID.String()).Msg("ocr list query failed")
		writeErr(w, r, err)
		return
	}
	resp := ocrListResponse{
		Pages:      pages,
		Status:     status,
		TotalPages: len(pages),
	}
	if len(pages) > 0 {
		var sum float32
		for _, p := range pages {
			sum += p.Confidence
		}
		resp.AvgConfidence = sum / float32(len(pages))
	}
	writeJSONStatus(w, http.StatusOK, resp)
}

// queryOCR pulls rows from ocr_results plus the most-recent
// ocr_processed_events status row (if any) so the frontend can show
// "OCR queued / running / completed / failed" without polling two
// endpoints.
func (h *OCRHandler) queryOCR(ctx context.Context, tenantID, versionID uuid.UUID) ([]OCRPage, string, error) {
	pages := []OCRPage{}
	status := "unknown"
	err := database.WithTenantTx(ctx, h.pool, tenantID, func(tx pgx.Tx) error {
		rows, qerr := tx.Query(ctx, `
			SELECT id::text, version_id::text, page_number,
			       text_content, confidence,
			       COALESCE(language, '') AS language,
			       bounding_boxes, processing_time_ms,
			       COALESCE(engine, '') AS engine,
			       created_at
			FROM ocr_results
			WHERE tenant_id = $1 AND version_id = $2
			ORDER BY page_number ASC
		`, tenantID, versionID)
		if qerr != nil {
			return qerr
		}
		defer rows.Close()
		for rows.Next() {
			var p OCRPage
			if scanErr := rows.Scan(
				&p.ID, &p.VersionID, &p.PageNumber,
				&p.TextContent, &p.Confidence, &p.Language,
				&p.BoundingBoxes, &p.ProcessingTimeMS,
				&p.Engine, &p.CreatedAt,
			); scanErr != nil {
				return scanErr
			}
			pages = append(pages, p)
		}
		// Status from ocr_processed_events — most recent row wins.
		// Status enum lives in the migration: 'enqueued','completed','failed'.
		var raw string
		serr := tx.QueryRow(ctx, `
			SELECT status FROM ocr_processed_events
			WHERE tenant_id = $1 AND version_id = $2
			ORDER BY processed_at DESC
			LIMIT 1
		`, tenantID, versionID).Scan(&raw)
		if serr == nil {
			switch raw {
			case "enqueued":
				status = "running"
			case "completed":
				status = "completed"
			case "failed":
				status = "failed"
			default:
				status = raw
			}
		} else if len(pages) > 0 {
			status = "completed"
		} else {
			status = "pending"
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return pages, status, nil
}

// rerun re-emits the OCR trigger event by writing a fresh outbox row
// for dms.version.uploaded.v1 with the same version payload. The
// intelligence service's intel-uploaded consumer picks it up and runs
// OCR again. Idempotency: ocr_processed_events dedupes on
// (tenant_id, event_id), so a fresh event_id forces a re-process.
//
// Permission: requires admin/owner role on the tenant for now —
// re-OCR is expensive (Surya can take ~30s/page) and shouldn't be
// triggerable by every member.
func (h *OCRHandler) rerun(w http.ResponseWriter, r *http.Request) {
	tenantID, err := auth.GetTenantID(r.Context())
	if err != nil || tenantID == uuid.Nil {
		writeErr(w, r, vdmserr.ErrUnauthorized)
		return
	}
	role := auth.GetUserRole(r.Context())
	if role != "owner" && role != "admin" && role != "compliance_officer" {
		writeErr(w, r, vdmserr.ErrForbidden)
		return
	}
	docID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("document_id", "not a uuid"))
		return
	}
	versionID, err := uuid.Parse(r.PathValue("vid"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("version_id", "not a uuid"))
		return
	}
	// Build a minimal event payload that mirrors what the document
	// service emits on a real upload (see services/document/internal/
	// service/documents.go::CreateVersion). The intelligence
	// consumer reads tenant_id / version_id / document_id from this.
	eventID, err := uuid.NewV7()
	if err != nil {
		writeErr(w, r, err)
		return
	}
	payload := map[string]any{
		"version_id":  versionID.String(),
		"document_id": docID.String(),
		"tenant_id":   tenantID.String(),
		"reason":      "manual_rerun",
	}
	body, _ := json.Marshal(payload)
	ctx := r.Context()
	err = database.WithTenantTx(ctx, h.pool, tenantID, func(tx pgx.Tx) error {
		_, ierr := tx.Exec(ctx, `
			INSERT INTO outbox (id, tenant_id, event_type, aggregate_type, aggregate_id, payload)
			VALUES ($1, $2, $3, 'version', $4, $5)
		`, eventID, tenantID, "dms.version.uploaded.v1", versionID, body)
		return ierr
	})
	if err != nil {
		h.log.Error().Err(err).Msg("ocr rerun outbox insert failed")
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusAccepted, map[string]any{
		"status":    "queued",
		"event_id":  eventID.String(),
	})
}
