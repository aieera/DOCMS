// ADR 0037 — DSR SLA monitor.
//
//	POST /internal/v1/dsr/sla-sweep
//
// Hourly cron. Scans privacy_dsr_requests against due_at; emits two
// classes of notification:
//
//   - dms.notify.dsr_sla_warning.v1 — request is within 12h of due_at,
//     not yet completed/failed/blocked. Once per request per 12h
//     window (rate-limited via privacy_dsr_requests.last_sla_notified_at).
//   - dms.notify.dsr_sla_breach.v1 — due_at < now(). Same rate-limit.
//     Compliance officers + owners get paged; alertmanager wires the
//     prom alert separately.
//
// Auth: shared-secret VAULTDMS_INTERNAL_API_KEY in Authorization header,
// same pattern as the disposition executor and retention sweep.

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

	"github.com/vaultdms/vaultdms/pkg/database"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
)

// DSRSLAHandler exposes the cron entry point.
type DSRSLAHandler struct {
	pool   *pgxpool.Pool
	outbox *database.OutboxRepository
	log    zerolog.Logger
}

// NewDSRSLAHandler constructs the handler.
func NewDSRSLAHandler(pool *pgxpool.Pool, outbox *database.OutboxRepository, log zerolog.Logger) *DSRSLAHandler {
	return &DSRSLAHandler{pool: pool, outbox: outbox, log: log}
}

// Register attaches the route.
func (h *DSRSLAHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /internal/v1/dsr/sla-sweep", h.sweep)
}

type slaSweepResult struct {
	Warnings  int `json:"warnings"`
	Breaches  int `json:"breaches"`
	Inspected int `json:"inspected"`
	Errors    int `json:"errors"`
}

// sweep runs the SLA scan globally — across all tenants in one pass.
// We use SET LOCAL row_security = off because the cron has no tenant
// identity; per-tenant emit happens by reading tenant_id from each row.
// This is the same pattern the storage reaper uses.
func (h *DSRSLAHandler) sweep(w http.ResponseWriter, r *http.Request) {
	if !checkInternalKey(r) {
		writeErr(w, r, vdmserr.ErrUnauthorized)
		return
	}
	res := slaSweepResult{}

	type slaRow struct {
		id       uuid.UUID
		tenantID uuid.UUID
		dueAt    time.Time
		typ      string
		email    string
	}
	var warnings, breaches []slaRow

	// Read in one bypass-RLS tx; emit in per-tenant txs (each
	// notification's outbox row needs the GUC set).
	now := time.Now().UTC()
	twelveHr := now.Add(12 * time.Hour)
	err := pgx.BeginFunc(r.Context(), h.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), "SET LOCAL row_security = off"); err != nil {
			return err
		}
		// Two sub-scans, one query: rows that need a warning emit and
		// rows past breach. Filtering last_sla_notified_at against the
		// 12h window naturally rate-limits per-row.
		rows, err := tx.Query(r.Context(), `
			SELECT id, tenant_id, due_at, request_type, subject_email,
			       (due_at < $1)        AS is_breach
			  FROM privacy_dsr_requests
			 WHERE status NOT IN ('completed', 'failed', 'blocked')
			   AND due_at < $2
			   AND (last_sla_notified_at IS NULL OR last_sla_notified_at < $3)
			 ORDER BY due_at ASC
			 LIMIT 1000`,
			now, twelveHr, now.Add(-12*time.Hour),
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row slaRow
			var isBreach bool
			if err := rows.Scan(&row.id, &row.tenantID, &row.dueAt, &row.typ, &row.email, &isBreach); err != nil {
				return err
			}
			res.Inspected++
			if isBreach {
				breaches = append(breaches, row)
			} else {
				warnings = append(warnings, row)
			}
		}
		return rows.Err()
	})
	if err != nil {
		h.log.Error().Err(err).Msg("dsr sla scan")
		writeErr(w, r, err)
		return
	}

	// Emit per-row in tenant-scoped txs. A failure on one row doesn't
	// abort the rest.
	for _, row := range warnings {
		if err := h.emitSlaEvent(r.Context(), row.tenantID, row, "dms.notify.dsr_sla_warning.v1", "warning"); err != nil {
			res.Errors++
			h.log.Error().Err(err).Str("request", row.id.String()).Msg("dsr sla warn emit")
			continue
		}
		res.Warnings++
	}
	for _, row := range breaches {
		if err := h.emitSlaEvent(r.Context(), row.tenantID, row, "dms.notify.dsr_sla_breach.v1", "breach"); err != nil {
			res.Errors++
			h.log.Error().Err(err).Str("request", row.id.String()).Msg("dsr sla breach emit")
			continue
		}
		res.Breaches++
	}

	h.log.Info().
		Int("inspected", res.Inspected).
		Int("warnings", res.Warnings).
		Int("breaches", res.Breaches).
		Int("errors", res.Errors).
		Msg("dsr sla sweep complete")
	writeJSONStatus(w, http.StatusOK, res)
}

func (h *DSRSLAHandler) emitSlaEvent(
	ctx context.Context, tenantID uuid.UUID,
	row struct {
		id       uuid.UUID
		tenantID uuid.UUID
		dueAt    time.Time
		typ      string
		email    string
	},
	subject, severity string,
) error {
	return database.WithTenantTx(ctx, h.pool, tenantID, func(tx pgx.Tx) error {
		// Look up the recipient set (compliance_officer + owner) for
		// this tenant. Empty list = quiet skip.
		userIDs, err := h.complianceUserIDs(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		if len(userIDs) == 0 {
			// Nobody to notify — still update the rate-limit stamp so
			// we don't re-scan this row every minute. Compliance gap
			// is its own problem; logged on the audit trail.
			_, err := tx.Exec(ctx, `
				UPDATE privacy_dsr_requests
				   SET last_sla_notified_at = now()
				 WHERE tenant_id = $1 AND id = $2`,
				tenantID, row.id,
			)
			return err
		}
		title := "DSR approaching deadline"
		body := "A data subject request is within 12 hours of its 30-day GDPR deadline."
		if severity == "breach" {
			title = "DSR PAST DEADLINE"
			body = "A data subject request has exceeded its 30-day GDPR deadline. Action required immediately."
		}
		payload := map[string]any{
			"tenant_id":     tenantID.String(),
			"user_ids":      userIDs,
			"type":          "dsr.sla." + severity,
			"title":         title,
			"body":          body,
			"resource_type": "privacy_dsr_request",
			"resource_id":   row.id.String(),
			"due_at":        row.dueAt.Format(time.RFC3339),
			"request_type":  row.typ,
			"subject_email": row.email,
		}
		bodyJSON, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		evt := database.NewOutboxEvent(tenantID, subject, "privacy_dsr_request", row.id, bodyJSON)
		if err := h.outbox.Insert(ctx, tx, evt); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			UPDATE privacy_dsr_requests
			   SET last_sla_notified_at = now()
			 WHERE tenant_id = $1 AND id = $2`,
			tenantID, row.id,
		)
		return err
	})
}

// complianceUserIDs fetches the compliance_officer + owner role
// members for a tenant. Used as the SLA notification recipient set.
func (h *DSRSLAHandler) complianceUserIDs(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text
		  FROM users
		 WHERE tenant_id = $1
		   AND role IN ('compliance_officer', 'owner')
		   AND deleted_at IS NULL`,
		tenantID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
