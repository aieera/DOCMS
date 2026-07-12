// ADR 0085 — saved-search alert schedule helpers.
//
// One Temporal Schedule per saved-search alert. Bootstrap on worker
// startup walks the saved_searches table and ensures every row with
// notify=true has a schedule; idempotent via AlreadyExists swallow.
//
// The service-layer PATCH path (commit 4 follow-up) calls Create /
// Delete here as the user toggles the alert; this bootstrap covers
// the worker-restart case + tenants that existed before the feature
// shipped.
package workflows

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	"github.com/aieera/sedoc/pkg/database"
)

// SavedSearchAlertScheduleID returns the deterministic schedule id
// for a saved-search alert. Stable so the bootstrap can detect
// "already exists" and the per-row delete can locate the schedule
// without storing the id alongside.
func SavedSearchAlertScheduleID(savedSearchID string) string {
	return "saved-search-alert-" + savedSearchID
}

// CreateSavedSearchAlertSchedule registers a Temporal Schedule for
// the alert. Cron expression takes precedence over interval when
// set. Idempotent — AlreadyExists is swallowed so callers can run
// it on every PATCH that flips notify=true without checking first.
func CreateSavedSearchAlertSchedule(
	ctx context.Context,
	tc client.Client,
	taskQueue, savedSearchID, tenantID, cronExpr string,
	intervalMinutes int,
) error {
	if taskQueue == "" {
		taskQueue = ScheduleTaskQueue
	}
	id := SavedSearchAlertScheduleID(savedSearchID)
	spec := client.ScheduleSpec{TimeZoneName: "UTC"}
	if strings.TrimSpace(cronExpr) != "" {
		spec.CronExpressions = []string{cronExpr}
	} else {
		// Fall back to interval-minutes mode. ADR 0085 covers both
		// shapes; cron wins when set, this is the simpler knob.
		minutes := intervalMinutes
		if minutes <= 0 {
			minutes = 15
		}
		// Build a simple cron: every N minutes, anchored to UTC.
		// Temporal's Spec accepts cron strings via CronExpressions;
		// for sub-hour intervals the canonical shape is `*/N * * * *`.
		spec.CronExpressions = []string{fmt.Sprintf("*/%d * * * *", minutes)}
	}
	_, err := tc.ScheduleClient().Create(ctx, client.ScheduleOptions{
		ID:   id,
		Spec: spec,
		Action: &client.ScheduleWorkflowAction{
			ID:        "wf-" + id,
			Workflow:  SavedSearchAlertWorkflow,
			Args:      []any{SavedSearchAlertInput{SavedSearchID: savedSearchID, TenantID: tenantID}},
			TaskQueue: taskQueue,
		},
		Overlap: 1, // SKIP if a previous tick is still in flight.
	})
	if err == nil {
		return nil
	}
	var already *serviceerror.AlreadyExists
	if errors.As(err, &already) {
		return nil
	}
	if strings.Contains(err.Error(), "already exists") {
		return nil
	}
	return fmt.Errorf("create alert schedule %s: %w", id, err)
}

// DeleteSavedSearchAlertSchedule removes the schedule. Idempotent —
// NotFound is swallowed so a delete that races with a never-started
// alert doesn't surface a 5xx to the API caller.
func DeleteSavedSearchAlertSchedule(
	ctx context.Context,
	tc client.Client,
	savedSearchID string,
) error {
	id := SavedSearchAlertScheduleID(savedSearchID)
	handle := tc.ScheduleClient().GetHandle(ctx, id)
	err := handle.Delete(ctx)
	if err == nil {
		return nil
	}
	var notFound *serviceerror.NotFound
	if errors.As(err, &notFound) {
		return nil
	}
	if strings.Contains(err.Error(), "not found") {
		return nil
	}
	return fmt.Errorf("delete alert schedule %s: %w", id, err)
}

// SavedSearchAlertRow is one alert row collected by the per-tenant
// sweep — the minimal fields Reconcile/Register need.
type SavedSearchAlertRow struct {
	ID          string
	TenantID    string
	Notify      bool
	CronExpr    string
	IntervalMin int
}

// CollectSavedSearchAlertRows gathers every tenant's saved-search rows
// for the schedule sweeps. saved_searches is FORCE RLS, so a raw
// cross-tenant scan fails closed under the prod NOBYPASSRLS role
// (Wave A.1.a, issue #70) — and SET row_security=off is forbidden.
// The sanctioned shape: enumerate tenants from the organizations
// registry (deliberately no RLS — it IS the tenant list), then read
// each tenant's rows under its own app.current_tenant.
func CollectSavedSearchAlertRows(ctx context.Context, pool *pgxpool.Pool, notifyOnly bool) ([]SavedSearchAlertRow, error) {
	tenantRows, err := pool.Query(ctx, `SELECT id FROM organizations`)
	if err != nil {
		return nil, fmt.Errorf("enumerate tenants: %w", err)
	}
	var tenants []uuid.UUID
	for tenantRows.Next() {
		var id uuid.UUID
		if err := tenantRows.Scan(&id); err != nil {
			tenantRows.Close()
			return nil, err
		}
		tenants = append(tenants, id)
	}
	tenantRows.Close()
	if err := tenantRows.Err(); err != nil {
		return nil, err
	}

	q := `SELECT id::text, tenant_id::text, notify,
	             COALESCE(alert_frequency_cron, ''),
	             COALESCE(notify_interval_minutes, 15)
	        FROM saved_searches`
	if notifyOnly {
		q += ` WHERE notify = true`
	}

	var out []SavedSearchAlertRow
	for _, tenant := range tenants {
		err := database.WithTenantTx(ctx, pool, tenant, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, q)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var r SavedSearchAlertRow
				if err := rows.Scan(&r.ID, &r.TenantID, &r.Notify, &r.CronExpr, &r.IntervalMin); err != nil {
					return err
				}
				out = append(out, r)
			}
			return rows.Err()
		})
		if err != nil {
			return nil, fmt.Errorf("collect alerts for tenant %s: %w", tenant, err)
		}
	}
	return out, nil
}

// ReconcileSavedSearchAlertSchedules walks the saved_searches table
// and brings the Temporal Schedule set in sync with the database:
//   - notify=true rows missing a schedule  → CreateSchedule
//   - notify=false rows with a schedule    → DeleteSchedule
//   - notify=true rows with a schedule already → no-op
//   - schedule whose saved-search row no longer exists → DeleteSchedule
//     (the row was deleted; the search service can't reach Temporal, so
//     this loop is where a deleted alert's schedule is cancelled — within
//     one reconcile tick rather than leaking a firing-but-no-op schedule)
//
// Idempotent + cheap (one DB scan + one Temporal Describe per row + one
// Schedule list for the orphan sweep). Run periodically by the worker
// (every 60s) so a search-service DELETE/PATCH becomes effective within a
// minute without the search service needing a Temporal client.
//
// Returns (created, deleted, err) for logging.
func ReconcileSavedSearchAlertSchedules(ctx context.Context, pool *pgxpool.Pool, tc client.Client, taskQueue string) (int, int, error) {
	alerts, err := CollectSavedSearchAlertRows(ctx, pool, false)
	if err != nil {
		return 0, 0, err
	}

	created, deleted := 0, 0
	existing := map[string]bool{} // every saved-search id seen this scan
	sc := tc.ScheduleClient()
	for _, r := range alerts {
		existing[r.ID] = true
		schedID := SavedSearchAlertScheduleID(r.ID)
		handle := sc.GetHandle(ctx, schedID)
		_, descErr := handle.Describe(ctx)
		exists := descErr == nil

		switch {
		case r.Notify && !exists:
			if err := CreateSavedSearchAlertSchedule(ctx, tc, taskQueue, r.ID, r.TenantID, r.CronExpr, r.IntervalMin); err != nil {
				return created, deleted, err
			}
			created++
		case !r.Notify && exists:
			if err := DeleteSavedSearchAlertSchedule(ctx, tc, r.ID); err != nil {
				return created, deleted, err
			}
			deleted++
		}
	}

	// Orphan sweep: any saved-search-alert schedule whose row was deleted
	// gets cancelled here. List the schedules, strip the deterministic
	// prefix, and delete the ones with no surviving row.
	iter, err := sc.List(ctx, client.ScheduleListOptions{})
	if err != nil {
		return created, deleted, fmt.Errorf("list schedules for orphan sweep: %w", err)
	}
	const prefix = "saved-search-alert-"
	for iter.HasNext() {
		entry, err := iter.Next()
		if err != nil {
			return created, deleted, err
		}
		if !strings.HasPrefix(entry.ID, prefix) {
			continue
		}
		ssID := strings.TrimPrefix(entry.ID, prefix)
		if existing[ssID] {
			continue // row still present
		}
		if err := DeleteSavedSearchAlertSchedule(ctx, tc, ssID); err != nil {
			return created, deleted, err
		}
		deleted++
	}
	return created, deleted, nil
}

// RegisterSavedSearchAlertSchedules walks notify=true saved searches
// and bootstraps a schedule for each. Returns the count newly
// created. Failure here is logged-but-not-fatal in the worker (the
// data-plane workflow runs are still served on demand).
func RegisterSavedSearchAlertSchedules(ctx context.Context, pool *pgxpool.Pool, tc client.Client, taskQueue string) (int, error) {
	alerts, err := CollectSavedSearchAlertRows(ctx, pool, true)
	if err != nil {
		return 0, err
	}

	created := 0
	for _, r := range alerts {
		// Existing-schedule path returns nil; only count actual creates.
		// Probe with a Describe before Create so the count is accurate.
		// (AlreadyExists path inside Create still works as the
		// idempotency net.)
		handle := tc.ScheduleClient().GetHandle(ctx, SavedSearchAlertScheduleID(r.ID))
		if _, err := handle.Describe(ctx); err == nil {
			continue // already exists; not newly created
		}
		if err := CreateSavedSearchAlertSchedule(ctx, tc, taskQueue, r.ID, r.TenantID, r.CronExpr, r.IntervalMin); err != nil {
			return created, err
		}
		created++
	}
	return created, nil
}
