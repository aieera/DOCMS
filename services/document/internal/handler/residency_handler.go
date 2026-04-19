// Wave 8 Prompt 8.4 — residency dashboard + migrate endpoints.
//
//	GET  /api/v1/residency/stats       per-region counts for the tenant
//	POST /api/v1/residency/migrations  queue a migrate workflow
//	GET  /api/v1/residency/migrations  list recent migrations
//	GET  /api/v1/residency/migrations/{id}
//
// Migrations are tracked in the `residency_migrations` +
// `residency_migration_items` tables (see migration 000007). The
// workflow is best-effort-dispatched; if Temporal is unreachable the
// row stays `pending` and an operator can redispatch.
package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"go.temporal.io/sdk/client"

	"github.com/vaultdms/vaultdms/pkg/database"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
)

const residencyTaskQueue = "vaultdms-default"

// ResidencyHandler mounts /residency REST endpoints.
type ResidencyHandler struct {
	pool *pgxpool.Pool
	tc   client.Client
	log  zerolog.Logger
}

// NewResidencyHandler constructs the handler.
func NewResidencyHandler(pool *pgxpool.Pool, tc client.Client, log zerolog.Logger) *ResidencyHandler {
	return &ResidencyHandler{pool: pool, tc: tc, log: log}
}

// Register attaches routes to the mux.
func (h *ResidencyHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/residency/stats", h.stats)
	mux.HandleFunc("POST /api/v1/residency/migrations", h.createMigration)
	mux.HandleFunc("GET /api/v1/residency/migrations", h.listMigrations)
	mux.HandleFunc("GET /api/v1/residency/migrations/{id}", h.getMigration)
}

type regionRow struct {
	Region    string `json:"region"`
	DocCount  int64  `json:"doc_count"`
	BlobBytes int64  `json:"blob_bytes"`
}

func (h *ResidencyHandler) stats(w http.ResponseWriter, r *http.Request) {
	tenantID, _, ok := callers(w, r)
	if !ok {
		return
	}
	out := []regionRow{}
	err := database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			WITH doc_regions AS (
			    SELECT region_pin AS region, COUNT(*) AS doc_count
			      FROM documents
			     WHERE tenant_id = $1 AND deleted_at IS NULL
			     GROUP BY region_pin
			),
			blob_regions AS (
			    SELECT storage_region AS region, COALESCE(SUM(size_bytes), 0) AS bytes
			      FROM content_blobs WHERE tenant_id = $1
			     GROUP BY storage_region
			)
			SELECT COALESCE(d.region, b.region), COALESCE(d.doc_count, 0), COALESCE(b.bytes, 0)
			  FROM doc_regions d FULL OUTER JOIN blob_regions b ON d.region = b.region
			 ORDER BY 1`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row regionRow
			if err := rows.Scan(&row.Region, &row.DocCount, &row.BlobBytes); err != nil {
				return err
			}
			out = append(out, row)
		}
		return rows.Err()
	})
	if err != nil {
		writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	writeJSONStatus(w, http.StatusOK, out)
}

type createMigrationBody struct {
	SourceRegion        string `json:"source_region"`
	TargetRegion        string `json:"target_region"`
	FilterWorkspace     string `json:"filter_workspace,omitempty"`
	FilterDocumentClass string `json:"filter_document_class,omitempty"`
	BatchSize           int    `json:"batch_size,omitempty"`
}

func (h *ResidencyHandler) createMigration(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := callers(w, r)
	if !ok {
		return
	}
	var body createMigrationBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	if body.SourceRegion == "" || body.TargetRegion == "" {
		writeErr(w, r, vdmserr.Validation("regions", "source_region and target_region required"))
		return
	}
	if body.SourceRegion == body.TargetRegion {
		writeErr(w, r, vdmserr.Validation("target_region", "must differ from source"))
		return
	}

	migrationID, err := uuid.NewV7()
	if err != nil {
		writeErr(w, r, vdmserr.Wrap(vdmserr.ErrInternal, err))
		return
	}

	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(r.Context(), `
			INSERT INTO residency_migrations
			    (tenant_id, id, source_region, target_region, filter_workspace,
			     filter_document_class, initiated_by, status)
			VALUES ($1,$2,$3,$4,NULLIF($5,'')::uuid,NULLIF($6,''),$7,'pending')`,
			tenantID, migrationID, body.SourceRegion, body.TargetRegion,
			body.FilterWorkspace, body.FilterDocumentClass, userID,
		)
		return err
	})
	if err != nil {
		writeErr(w, r, vdmserr.FromPgError(err))
		return
	}

	// Best-effort workflow dispatch.
	if h.tc != nil {
		_, werr := h.tc.ExecuteWorkflow(r.Context(),
			client.StartWorkflowOptions{
				ID:        "residency-" + migrationID.String(),
				TaskQueue: residencyTaskQueue,
			},
			"ResidencyMigrationWorkflow",
			map[string]any{
				"tenant_id":             tenantID.String(),
				"migration_id":          migrationID.String(),
				"source_region":         body.SourceRegion,
				"target_region":         body.TargetRegion,
				"filter_workspace":      body.FilterWorkspace,
				"filter_document_class": body.FilterDocumentClass,
				"batch_size":            body.BatchSize,
			},
		)
		if werr != nil {
			h.log.Warn().Err(werr).Str("migration_id", migrationID.String()).
				Msg("residency workflow start failed; row stays pending")
		}
	}

	writeJSONStatus(w, http.StatusAccepted, map[string]any{
		"id":             migrationID.String(),
		"status":         "pending",
		"source_region":  body.SourceRegion,
		"target_region":  body.TargetRegion,
		"created_at":     time.Now().UTC().Format(time.RFC3339),
	})
}

func (h *ResidencyHandler) listMigrations(w http.ResponseWriter, r *http.Request) {
	tenantID, _, ok := callers(w, r)
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	switch status {
	case "", "pending", "running", "completed", "failed", "cancelled":
	default:
		writeErr(w, r, vdmserr.Validation("status", "invalid"))
		return
	}
	sql := `SELECT id::text, source_region, target_region, status, total_docs, moved_docs, failed_docs,
	               created_at, completed_at
	          FROM residency_migrations WHERE tenant_id = $1`
	args := []any{tenantID}
	if status != "" {
		sql += " AND status = $2"
		args = append(args, status)
	}
	sql += " ORDER BY created_at DESC LIMIT 200"

	out := []map[string]any{}
	err := database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, src, tgt, st string
			var total, moved, failed int
			var created time.Time
			var completed *time.Time
			if err := rows.Scan(&id, &src, &tgt, &st, &total, &moved, &failed, &created, &completed); err != nil {
				return err
			}
			m := map[string]any{
				"id":            id,
				"source_region": src,
				"target_region": tgt,
				"status":        st,
				"total_docs":    total,
				"moved_docs":    moved,
				"failed_docs":   failed,
				"created_at":    created,
			}
			if completed != nil {
				m["completed_at"] = completed
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	if err != nil {
		writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	writeJSONStatus(w, http.StatusOK, out)
}

func (h *ResidencyHandler) getMigration(w http.ResponseWriter, r *http.Request) {
	tenantID, _, ok := callers(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	res := map[string]any{}
	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		var src, tgt, st string
		var total, moved, failed int
		var created time.Time
		var completed *time.Time
		var workflowRun, errSummary *string
		err := tx.QueryRow(r.Context(), `
			SELECT source_region, target_region, status, total_docs, moved_docs, failed_docs,
			       created_at, completed_at, workflow_run_id, error_summary
			  FROM residency_migrations WHERE tenant_id = $1 AND id = $2`,
			tenantID, id,
		).Scan(&src, &tgt, &st, &total, &moved, &failed, &created, &completed, &workflowRun, &errSummary)
		if err != nil {
			return err
		}
		res["id"] = id.String()
		res["source_region"] = src
		res["target_region"] = tgt
		res["status"] = st
		res["total_docs"] = total
		res["moved_docs"] = moved
		res["failed_docs"] = failed
		res["created_at"] = created
		if completed != nil {
			res["completed_at"] = completed
		}
		if workflowRun != nil {
			res["workflow_run_id"] = *workflowRun
		}
		if errSummary != nil {
			res["error_summary"] = *errSummary
		}
		// Per-status item counts — useful for progress bars.
		rows, err := tx.Query(r.Context(), `
			SELECT status, COUNT(*) FROM residency_migration_items
			 WHERE migration_id = $1 GROUP BY status`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		counts := map[string]int{}
		for rows.Next() {
			var s string
			var c int
			if err := rows.Scan(&s, &c); err != nil {
				return err
			}
			counts[s] = c
		}
		res["items"] = counts
		return nil
	})
	if err != nil {
		writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	writeJSONStatus(w, http.StatusOK, res)
}

// unused import guard — keeps `fmt`, `strings` available for future
// extensions without churn on this file.
var _ = fmt.Sprintf
var _ = strings.TrimSpace
