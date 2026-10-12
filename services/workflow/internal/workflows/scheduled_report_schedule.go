// ADR 0119 — scheduled-report schedule helpers, mirroring the
// saved-search-alert pattern: one Temporal Schedule per enabled report,
// deterministic id, and a periodic reconcile that syncs the schedule set
// to the saved_reports table (so the document service never needs a
// Temporal client — a PATCH becomes effective within one reconcile tick).
package workflows

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	"github.com/aieera/sedoc/pkg/database"
)

// ScheduledReportScheduleID returns the deterministic schedule id.
func ScheduledReportScheduleID(reportID string) string {
	return "scheduled-report-" + reportID
}

// CreateScheduledReportSchedule registers the schedule. Cron wins over
// interval-minutes. Idempotent (AlreadyExists swallowed).
func CreateScheduledReportSchedule(
	ctx context.Context,
	tc client.Client,
	taskQueue, reportID, tenantID, cronExpr string,
	intervalMinutes int,
) error {
	if taskQueue == "" {
		taskQueue = ScheduleTaskQueue
	}
	id := ScheduledReportScheduleID(reportID)
	spec := client.ScheduleSpec{TimeZoneName: "UTC"}
	if strings.TrimSpace(cronExpr) != "" {
		spec.CronExpressions = []string{cronExpr}
	} else {
		minutes := intervalMinutes
		if minutes <= 0 {
			minutes = 60
		}
		spec.CronExpressions = []string{fmt.Sprintf("*/%d * * * *", minutes)}
	}
	_, err := tc.ScheduleClient().Create(ctx, client.ScheduleOptions{
		ID:   id,
		Spec: spec,
		Action: &client.ScheduleWorkflowAction{
			ID:        "wf-" + id,
			Workflow:  ScheduledReportWorkflow,
			Args:      []any{ScheduledReportInput{ReportID: reportID, TenantID: tenantID}},
			TaskQueue: taskQueue,
		},
		Overlap: 1, // SKIP if the previous tick is still running.
	})
	if err == nil {
		return nil
	}
	var already *serviceerror.AlreadyExists
	if errors.As(err, &already) || strings.Contains(err.Error(), "already exists") {
		return nil
	}
	return fmt.Errorf("create report schedule %s: %w", id, err)
}

// DeleteScheduledReportSchedule removes the schedule (NotFound swallowed).
func DeleteScheduledReportSchedule(ctx context.Context, tc client.Client, reportID string) error {
	id := ScheduledReportScheduleID(reportID)
	err := tc.ScheduleClient().GetHandle(ctx, id).Delete(ctx)
	if err == nil {
		return nil
	}
	var notFound *serviceerror.NotFound
	if errors.As(err, &notFound) || strings.Contains(err.Error(), "not found") {
		return nil
	}
	return fmt.Errorf("delete report schedule %s: %w", id, err)
}

// ScheduledReportRow is one saved_reports row collected by the per-tenant
// sweep — the minimal fields Reconcile needs.
type ScheduledReportRow struct {
	ID          string
	TenantID    string
	Enabled     bool
	CronExpr    string
	IntervalMin int
}

// CollectScheduledReportRows gathers every tenant's saved_reports rows for
// the schedule sweep. saved_reports is FORCE RLS, so the former raw-pool
// cross-tenant scan failed closed under the prod NOBYPASSRLS role (L-11):
// it errored or read zero rows, no schedule was ever created, and on a
// zero-row read the orphan sweep deleted every report schedule. The
// sanctioned shape: enumerate tenants from the non-RLS organizations
// registry, then read each tenant's rows under its own app.current_tenant.
// Any tenant's failure aborts the whole collection, so the orphan sweep
// never runs on a partial view.
func CollectScheduledReportRows(ctx context.Context, pool *pgxpool.Pool) ([]ScheduledReportRow, error) {
	tenants, err := database.ListTenantIDs(ctx, pool)
	if err != nil {
		return nil, err
	}
	var out []ScheduledReportRow
	for _, tenant := range tenants {
		err := database.WithTenantTx(ctx, pool, tenant, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `
				SELECT id::text, tenant_id::text, schedule_enabled,
				       COALESCE(schedule_cron, ''),
				       COALESCE(schedule_interval_minutes, 0)
				  FROM saved_reports
			`)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var r ScheduledReportRow
				if err := rows.Scan(&r.ID, &r.TenantID, &r.Enabled, &r.CronExpr, &r.IntervalMin); err != nil {
					return err
				}
				out = append(out, r)
			}
			return rows.Err()
		})
		if err != nil {
			return nil, fmt.Errorf("collect scheduled reports for tenant %s: %w", tenant, err)
		}
	}
	return out, nil
}

// ReconcileScheduledReportSchedules syncs Temporal Schedules to the
// saved_reports table: enabled-without-schedule → create,
// disabled-with-schedule → delete, schedule-without-row → delete
// (orphan sweep). Returns (created, deleted, err).
func ReconcileScheduledReportSchedules(ctx context.Context, pool *pgxpool.Pool, tc client.Client, taskQueue string) (int, int, error) {
	reports, err := CollectScheduledReportRows(ctx, pool)
	if err != nil {
		return 0, 0, err
	}

	created, deleted := 0, 0
	existing := map[string]bool{}
	sc := tc.ScheduleClient()
	for _, r := range reports {
		existing[r.ID] = true
		handle := sc.GetHandle(ctx, ScheduledReportScheduleID(r.ID))
		_, descErr := handle.Describe(ctx)
		exists := descErr == nil
		switch {
		case r.Enabled && !exists:
			if createErr := CreateScheduledReportSchedule(ctx, tc, taskQueue, r.ID, r.TenantID, r.CronExpr, r.IntervalMin); createErr != nil {
				return created, deleted, createErr
			}
			created++
		case !r.Enabled && exists:
			if deleteErr := DeleteScheduledReportSchedule(ctx, tc, r.ID); deleteErr != nil {
				return created, deleted, deleteErr
			}
			deleted++
		}
	}

	iter, err := sc.List(ctx, client.ScheduleListOptions{})
	if err != nil {
		return created, deleted, fmt.Errorf("list schedules for orphan sweep: %w", err)
	}
	const prefix = "scheduled-report-"
	for iter.HasNext() {
		entry, err := iter.Next()
		if err != nil {
			return created, deleted, err
		}
		if !strings.HasPrefix(entry.ID, prefix) {
			continue
		}
		reportID := strings.TrimPrefix(entry.ID, prefix)
		if existing[reportID] {
			continue
		}
		if err := DeleteScheduledReportSchedule(ctx, tc, reportID); err != nil {
			return created, deleted, err
		}
		deleted++
	}
	return created, deleted, nil
}
