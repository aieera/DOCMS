// Wave 11.5 — document redaction endpoint.
//
//	POST /api/v1/documents/{id}/redact
//	  body: { reason, version_id?, regions: [...], entity_types: [...] }
//
// Policy invariants:
//
//  1. Any active legal hold on the document → 423 Locked. Mirrors
//     the DeleteDocument hold gate (Wave 8.2).
//  2. Role gate: redaction is a compliance-sensitive mutation.
//     compliance_officer / admin / owner only.
//  3. Every call writes an append-only `document_redactions` row
//     and emits `dms.document.redacted.v1` via the outbox —
//     atomic with the row insert.
//
// Pixel-level redaction (PyMuPDF apply_redactions — burn black
// rectangles over regions) still runs in the intelligence service's
// Celery task queue. This endpoint orchestrates: hold-check, audit,
// domain event. A follow-up that fans the `dms.document.redacted.v1`
// event into an intelligence consumer lands in Wave 12.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/pkg/database"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/document/internal/compliance"
	"github.com/vaultdms/vaultdms/services/document/internal/model"
)

// RedactionHandler mounts /api/v1/documents/{id}/redact.
type RedactionHandler struct {
	pool  *pgxpool.Pool
	holds *compliance.HoldsService
	log   zerolog.Logger
}

// NewRedactionHandler constructs a handler.
func NewRedactionHandler(pool *pgxpool.Pool, holds *compliance.HoldsService, log zerolog.Logger) *RedactionHandler {
	return &RedactionHandler{pool: pool, holds: holds, log: log}
}

// Register attaches the route.
func (h *RedactionHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/documents/{id}/redact", h.apply)
}

type redactionRegion struct {
	Page  int     `json:"page"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	Width float64 `json:"width"`
	Height float64 `json:"height"`
	Label string  `json:"label,omitempty"`
}

type applyRedactionBody struct {
	Reason       string            `json:"reason"`
	VersionID    string            `json:"version_id,omitempty"`
	Regions      []redactionRegion `json:"regions"`
	EntityTypes  []string          `json:"entity_types,omitempty"`
}

func (h *RedactionHandler) apply(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "compliance_officer", "admin", "owner") {
		return
	}
	docID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "not a uuid"))
		return
	}
	var body applyRedactionBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	if body.Reason == "" {
		writeErr(w, r, vdmserr.Validation("reason", "required"))
		return
	}
	if len(body.Regions) == 0 && len(body.EntityTypes) == 0 {
		writeErr(w, r, vdmserr.Validation("regions|entity_types", "at least one required"))
		return
	}

	// Parse version_id here (before hold I/O) so bad client input
	// surfaces as 400 without touching the DB.
	var versionID *uuid.UUID
	if body.VersionID != "" {
		vid, err := uuid.Parse(body.VersionID)
		if err != nil {
			writeErr(w, r, vdmserr.Validation("version_id", "not a uuid"))
			return
		}
		versionID = &vid
	}

	// Hold gate — the spec invariant: a document under an active
	// legal hold can't be redacted. Returns 423 Locked via
	// ErrLegalHold (Wave 8.2 mapping). `h.holds == nil` is a
	// misconfiguration the tests construct deliberately; guard
	// rather than panic.
	if h.holds == nil {
		writeErr(w, r, vdmserr.Internal("holds service not wired"))
		return
	}
	held, err := h.holds.AnyActiveHoldFor(r.Context(), tenantID, docID)
	if err != nil {
		writeErr(w, r, vdmserr.Wrap(vdmserr.ErrInternal, err))
		return
	}
	if held {
		writeErr(w, r, vdmserr.ErrLegalHold)
		return
	}

	redactionID, _ := uuid.NewV7()
	regionsJSON, _ := json.Marshal(body.Regions)
	entityTypes := body.EntityTypes
	if entityTypes == nil {
		entityTypes = []string{}
	}

	// Single tenant-scoped tx: insert audit row + outbox event.
	// Either both land or neither does.
	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(r.Context(), `
			INSERT INTO document_redactions
			    (tenant_id, id, document_id, version_id, applied_by,
			     reason, regions, entity_types, status)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'queued')`,
			tenantID, redactionID, docID, versionID, userID,
			body.Reason, regionsJSON, entityTypes)
		if err != nil {
			return vdmserr.FromPgError(err)
		}
		evt, err := model.NewOutboxEvent(tenantID,
			"dms.document.redacted.v1", "document", docID,
			map[string]any{
				"tenant_id":     tenantID.String(),
				"document_id":   docID.String(),
				"version_id":    body.VersionID,
				"redaction_id":  redactionID.String(),
				"reason":        body.Reason,
				"region_count":  len(body.Regions),
				"entity_types":  entityTypes,
				"applied_by":    userID.String(),
			})
		if err != nil {
			return err
		}
		return insertOutboxInTx(r.Context(), tx, evt)
	})
	if err != nil {
		if errors.Is(err, vdmserr.ErrLegalHold) {
			writeErr(w, r, vdmserr.ErrLegalHold)
			return
		}
		writeErr(w, r, err)
		return
	}

	writeJSONStatus(w, http.StatusAccepted, map[string]any{
		"redaction_id": redactionID.String(),
		"document_id":  docID.String(),
		"status":       "queued",
		"created_at":   time.Now().UTC().Format(time.RFC3339),
		"note":         "pixel-level redaction runs asynchronously in the intelligence service",
	})
}

// insertOutboxInTx writes the model.OutboxEvent via the outbox
// column shape (no `subject` column).
func insertOutboxInTx(ctx context.Context, tx pgx.Tx, evt *model.OutboxEvent) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO outbox (id, tenant_id, event_type, aggregate_type, aggregate_id, payload, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		evt.ID, evt.TenantID, evt.EventType, evt.AggregateType,
		evt.AggregateID, evt.Payload, evt.CreatedAt)
	return err
}
