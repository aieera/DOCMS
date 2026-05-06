// ADR 0068 — saved-search alert schedule helpers.
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

	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
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
		// Fall back to interval-minutes mode. ADR 0068 covers both
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

// ReconcileSavedSearchAlertSchedules walks the saved_searches table
// and brings the Temporal Schedule set in sync with the database:
//   - notify=true rows missing a schedule  → CreateSchedule
//   - notify=false rows with a schedule    → DeleteSchedule
//   - notify=true rows with a schedule already → no-op
//
// Idempotent + cheap (one DB scan + one Temporal Describe per row).
// Run periodically by the worker (every 60s) so a search-service
// PATCH that flips notify becomes effective within a minute without
// the search service needing a Temporal client.
//
// Returns (created, deleted, err) for logging.
func ReconcileSavedSearchAlertSchedules(ctx context.Context, pool *pgxpool.Pool, tc client.Client, taskQueue string) (int, int, error) {
	rows, err := pool.Query(ctx, `
		SELECT id::text, tenant_id::text, notify,
		       COALESCE(alert_frequency_cron, ''),
		       COALESCE(notify_interval_minutes, 15)
		  FROM saved_searches
	`)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()

	created, deleted := 0, 0
	sc := tc.ScheduleClient()
	for rows.Next() {
		var id, tenantID, cronExpr string
		var notify bool
		var intervalMin int
		if err := rows.Scan(&id, &tenantID, &notify, &cronExpr, &intervalMin); err != nil {
			return created, deleted, err
		}
		schedID := SavedSearchAlertScheduleID(id)
		handle := sc.GetHandle(ctx, schedID)
		_, descErr := handle.Describe(ctx)
		exists := descErr == nil

		switch {
		case notify && !exists:
			if err := CreateSavedSearchAlertSchedule(ctx, tc, taskQueue, id, tenantID, cronExpr, intervalMin); err != nil {
				return created, deleted, err
			}
			created++
		case !notify && exists:
			if err := DeleteSavedSearchAlertSchedule(ctx, tc, id); err != nil {
				return created, deleted, err
			}
			deleted++
		}
	}
	return created, deleted, rows.Err()
}

// RegisterSavedSearchAlertSchedules walks notify=true saved searches
// and bootstraps a schedule for each. Returns the count newly
// created. Failure here is logged-but-not-fatal in the worker (the
// data-plane workflow runs are still served on demand).
func RegisterSavedSearchAlertSchedules(ctx context.Context, pool *pgxpool.Pool, tc client.Client, taskQueue string) (int, error) {
	rows, err := pool.Query(ctx, `
		SELECT id::text, tenant_id::text,
		       COALESCE(alert_frequency_cron, ''),
		       COALESCE(notify_interval_minutes, 15)
		  FROM saved_searches
		 WHERE notify = true
	`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	created := 0
	for rows.Next() {
		var id, tenantID, cronExpr string
		var intervalMin int
		if err := rows.Scan(&id, &tenantID, &cronExpr, &intervalMin); err != nil {
			return created, err
		}
		// Existing-schedule path returns nil; only count actual creates.
		// Probe with a Describe before Create so the count is accurate.
		// (AlreadyExists path inside Create still works as the
		// idempotency net.)
		handle := tc.ScheduleClient().GetHandle(ctx, SavedSearchAlertScheduleID(id))
		if _, err := handle.Describe(ctx); err == nil {
			continue // already exists; not newly created
		}
		if err := CreateSavedSearchAlertSchedule(ctx, tc, taskQueue, id, tenantID, cronExpr, intervalMin); err != nil {
			return created, err
		}
		created++
	}
	return created, rows.Err()
}
