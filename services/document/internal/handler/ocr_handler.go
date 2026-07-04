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
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
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
	mux.HandleFunc("PATCH /api/v1/documents/{id}/versions/{vid}/ocr/{page}", h.correct)
}

// OCRPage is the wire shape returned to the frontend. Mirrors the
// ocr_results columns plus a denormalized engine string so the UI
// can show "Surya · 96.2%" without joining anything else.
type OCRPage struct {
	ID            string          `json:"id"`
	VersionID     string          `json:"version_id"`
	PageNumber    int             `json:"page_number"`
	TextContent   string          `json:"text_content"`
	Confidence    float32         `json:"confidence"`
	Language      string          `json:"language,omitempty"`
	BoundingBoxes json.RawMessage `json:"bounding_boxes"`
	// WordBoxes carries the per-word PDF coordinates the entity
	// overlay needs (ADR 0078 follow-up). Empty array when the
	// engine doesn't produce them (Surya line-only path).
	WordBoxes        json.RawMessage `json:"word_boxes"`
	ProcessingTimeMS *int            `json:"processing_time_ms,omitempty"`
	Engine           string          `json:"engine,omitempty"`
	// CorrectedText is the human-corrected transcription when a reviewer
	// has edited this page; null/empty otherwise. The UI shows it in the
	// manual-correction field and prefers it over TextContent.
	CorrectedText *string    `json:"corrected_text,omitempty"`
	CorrectedAt   *time.Time `json:"corrected_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

type ocrListResponse struct {
	Pages         []OCRPage `json:"pages"`
	Status        string    `json:"status"` // pending | running | completed | failed | unknown
	TotalPages    int       `json:"total_pages"`
	AvgConfidence float32   `json:"avg_confidence"`
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
			       bounding_boxes,
			       COALESCE(word_boxes, '[]'::jsonb) AS word_boxes,
			       processing_time_ms,
			       COALESCE(engine, '') AS engine,
			       corrected_text, corrected_at,
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
				&p.BoundingBoxes, &p.WordBoxes, &p.ProcessingTimeMS,
				&p.Engine, &p.CorrectedText, &p.CorrectedAt, &p.CreatedAt,
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
	// Build the SAME event payload shape the document service emits
	// on a real upload (services/document/internal/service/documents.go
	// CreateVersion). The intelligence consumer's _resolve_storage
	// expects storage_uri (or bucket+key), and mime_type is checked
	// against OCR_MIMES. A minimal {tenant,doc,version} payload causes
	// the consumer to msg.term() the event as poisoned — no retry,
	// no row in ocr_processed_events, silent drop.
	//
	// We look up the version's blob to populate storage_uri / mime /
	// size / sha. content_blobs is owned by services/storage but
	// reachable via the shared Postgres.
	eventID, err := uuid.NewV7()
	if err != nil {
		writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	var (
		blobID     string
		mimeType   string
		sizeBytes  int64
		sha256Hash string
		bucket     string
		storageKey string
		versionNum int
	)
	err = database.WithTenantTx(ctx, h.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT v.content_blob_id::text, COALESCE(v.mime_type,''), v.size_bytes,
			       COALESCE(v.sha256_hash,''), b.storage_bucket, b.storage_key,
			       v.version_number
			FROM document_versions v
			JOIN content_blobs b ON b.tenant_id = v.tenant_id AND b.id = v.content_blob_id
			WHERE v.tenant_id = $1 AND v.id = $2
		`, tenantID, versionID).Scan(&blobID, &mimeType, &sizeBytes, &sha256Hash,
			&bucket, &storageKey, &versionNum)
	})
	if err != nil {
		h.log.Error().Err(err).Str("version", versionID.String()).Msg("ocr rerun: version+blob lookup failed")
		writeErr(w, r, err)
		return
	}
	storageURI := "s3://" + bucket + "/" + storageKey
	payload := map[string]any{
		"event_id":            eventID.String(),
		"tenant_id":           tenantID.String(),
		"document_id":         docID.String(),
		"version_id":          versionID.String(),
		"version_number":      versionNum,
		"content_blob_id":     blobID,
		"storage_uri":         storageURI,
		"mime_type":           mimeType,
		"size_bytes":          sizeBytes,
		"sha256":              sha256Hash,
		"uploaded_by_user_id": "",
		"uploaded_at":         time.Now().UTC().Format(time.RFC3339),
		"reason":              "manual_rerun",
	}
	// ?force=<engine> overrides engine selection for this re-OCR. surya
	// bypasses the pymupdf text fast path for layout boxes; handwriting
	// routes the doc through TrOCR (ICR) merged with printed OCR; printed
	// forces Surya/Paddle; auto defers to the per-tenant/doc-type config.
	// Unknown values are ignored (the worker then resolves from config).
	switch fe := r.URL.Query().Get("force"); fe {
	case "surya", "printed", "handwriting", "auto":
		payload["force_engine"] = fe
	}
	body, _ := json.Marshal(payload)
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
		"status":   "queued",
		"event_id": eventID.String(),
	})
}

// correct stores a human-corrected transcription for one OCR page. The
// engine output in text_content is left untouched; corrected_text +
// corrected_by + corrected_at capture the override. Gated to the same
// roles as rerun (review is an admin/compliance action); tenant isolation
// is enforced by RLS. A document-level edit-permission check is a follow-up
// (this handler isn't wired to the document service).
//
//	PATCH /api/v1/documents/{id}/versions/{vid}/ocr/{page}
//	body: {"corrected_text": "..."}
func (h *OCRHandler) correct(w http.ResponseWriter, r *http.Request) {
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
	versionID, err := uuid.Parse(r.PathValue("vid"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("version_id", "not a uuid"))
		return
	}
	pageNo, err := strconv.Atoi(r.PathValue("page"))
	if err != nil || pageNo < 1 {
		writeErr(w, r, vdmserr.Validation("page", "must be a positive integer"))
		return
	}
	var body struct {
		CorrectedText string `json:"corrected_text"`
	}
	if derr := json.NewDecoder(r.Body).Decode(&body); derr != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	if len(body.CorrectedText) > 1<<20 {
		writeErr(w, r, vdmserr.Validation("corrected_text", "too large (max 1 MiB)"))
		return
	}
	// corrected_by is nullable — record the user when present, else leave null.
	var correctedBy *uuid.UUID
	if uid, uerr := auth.GetUserID(r.Context()); uerr == nil && uid != uuid.Nil {
		correctedBy = &uid
	}
	ctx := r.Context()
	var updated int64
	err = database.WithTenantTx(ctx, h.pool, tenantID, func(tx pgx.Tx) error {
		ct, eerr := tx.Exec(ctx, `
			UPDATE ocr_results
			   SET corrected_text = $3, corrected_by = $4, corrected_at = NOW()
			 WHERE tenant_id = $1 AND version_id = $2 AND page_number = $5
		`, tenantID, versionID, body.CorrectedText, correctedBy, pageNo)
		if eerr != nil {
			return eerr
		}
		updated = ct.RowsAffected()
		return nil
	})
	if err != nil {
		h.log.Error().Err(err).Str("version", versionID.String()).Int("page", pageNo).Msg("ocr correct failed")
		writeErr(w, r, err)
		return
	}
	if updated == 0 {
		writeErr(w, r, vdmserr.NotFound("ocr page not found"))
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"status": "saved", "page_number": pageNo})
}
