// compliance_overview_handler — tenant-scoped real-data feed for the
// /admin/compliance dashboard. Three signals:
//
//   - docs_by_state: COUNT(*) GROUP BY lifecycle_state (live docs only)
//   - storage_by_region: SUM(size_bytes) over content_blobs grouped by
//     the region embedded in storage_bucket (dms-<region>-<tier>)
//   - encryption_coverage: % of non-shredded blobs with encrypted_dek set
//
// Owner/admin only. RLS enforces the tenant scope; we still pass the
// tenant id explicitly so a mis-set GUC fails closed (0 rows).
package handler

import (
	"context"
	"net/http"
	"regexp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/pkg/database"
)

type ComplianceOverviewHandler struct {
	pool *pgxpool.Pool
}

func NewComplianceOverviewHandler(pool *pgxpool.Pool) *ComplianceOverviewHandler {
	return &ComplianceOverviewHandler{pool: pool}
}

func (h *ComplianceOverviewHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/compliance/overview", h.get)
}

type stateCount struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

type regionBytes struct {
	Name string `json:"name"`
	GB   int64  `json:"gb"`
}

type complianceOverview struct {
	DocsByState        []stateCount  `json:"docs_by_state"`
	StorageByRegion    []regionBytes `json:"storage_by_region"`
	EncryptionCoverage float64       `json:"encryption_coverage"`
	EncryptedBlobs     int64         `json:"encrypted_blobs"`
	TotalBlobs         int64         `json:"total_blobs"`
}

// Matches the "dms-<region>-<tier>" bucket convention so we can group
// storage by region without a separate metadata table. <region> is
// everything between the leading "dms-" and the final tier suffix.
var bucketRegionRE = regexp.MustCompile(`^dms-(.+?)-(hot|cold|archive|warm)$`)

func (h *ComplianceOverviewHandler) get(w http.ResponseWriter, r *http.Request) {
	ctx, tenantID, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	out := complianceOverview{
		DocsByState:     []stateCount{},
		StorageByRegion: []regionBytes{},
	}
	if err := database.WithTenantTx(ctx, h.pool, tenantID, func(tx pgx.Tx) error {
		if err := h.docsByState(ctx, tx, tenantID, &out); err != nil {
			return err
		}
		if err := h.storageByRegion(ctx, tx, tenantID, &out); err != nil {
			return err
		}
		return h.encryptionCoverage(ctx, tx, tenantID, &out)
	}); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, out)
}

func (h *ComplianceOverviewHandler) docsByState(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, out *complianceOverview) error {
	rows, err := tx.Query(ctx, `
		SELECT COALESCE(lifecycle_state, 'unknown') AS state, COUNT(*)
		FROM documents
		WHERE tenant_id = $1 AND deleted_at IS NULL
		GROUP BY state
		ORDER BY COUNT(*) DESC
	`, tenantID)
	if err != nil {
		return vdmserr.ErrInternal
	}
	defer rows.Close()
	for rows.Next() {
		var s stateCount
		if err := rows.Scan(&s.Name, &s.Count); err != nil {
			return err
		}
		out.DocsByState = append(out.DocsByState, s)
	}
	return rows.Err()
}

func (h *ComplianceOverviewHandler) storageByRegion(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, out *complianceOverview) error {
	rows, err := tx.Query(ctx, `
		SELECT storage_bucket, COALESCE(SUM(size_bytes), 0)
		FROM content_blobs
		WHERE tenant_id = $1 AND shredded_at IS NULL
		GROUP BY storage_bucket
	`, tenantID)
	if err != nil {
		return vdmserr.ErrInternal
	}
	defer rows.Close()
	byRegion := map[string]int64{}
	for rows.Next() {
		var bucket string
		var bytes int64
		if err := rows.Scan(&bucket, &bytes); err != nil {
			return err
		}
		region := "unknown"
		if m := bucketRegionRE.FindStringSubmatch(bucket); len(m) == 3 {
			region = m[1]
		}
		byRegion[region] += bytes
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for name, bytes := range byRegion {
		gb := bytes / (1024 * 1024 * 1024)
		// Round up to 1 GB so tiny dev tenants don't render as 0
		// next to a chart legend that says "GB" — the bar still
		// communicates "this region has content".
		if gb == 0 && bytes > 0 {
			gb = 1
		}
		out.StorageByRegion = append(out.StorageByRegion, regionBytes{Name: name, GB: gb})
	}
	return nil
}

func (h *ComplianceOverviewHandler) encryptionCoverage(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, out *complianceOverview) error {
	var encrypted, total int64
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FILTER (WHERE encrypted_dek IS NOT NULL),
		       COUNT(*)
		FROM content_blobs
		WHERE tenant_id = $1 AND shredded_at IS NULL
	`, tenantID).Scan(&encrypted, &total); err != nil {
		return vdmserr.ErrInternal
	}
	out.EncryptedBlobs = encrypted
	out.TotalBlobs = total
	if total > 0 {
		out.EncryptionCoverage = (float64(encrypted) / float64(total)) * 100.0
	}
	return nil
}
