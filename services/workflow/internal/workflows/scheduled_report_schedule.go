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

	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
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

// ReconcileScheduledReportSchedules syncs Temporal Schedules to the
// saved_reports table: enabled-without-schedule → create,
// disabled-with-schedule → delete, schedule-without-row → delete
// (orphan sweep). Returns (created, deleted, err).
func ReconcileScheduledReportSchedules(ctx context.Context, pool *pgxpool.Pool, tc client.Client, taskQueue string) (int, int, error) {
	rows, err := pool.Query(ctx, `
		SELECT id::text, tenant_id::text, schedule_enabled,
		       COALESCE(schedule_cron, ''),
		       COALESCE(schedule_interval_minutes, 0)
		  FROM saved_reports
	`)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()

	created, deleted := 0, 0
	existing := map[string]bool{}
	sc := tc.ScheduleClient()
	for rows.Next() {
		var id, tenantID, cronExpr string
		var enabled bool
		var intervalMin int
		if err := rows.Scan(&id, &tenantID, &enabled, &cronExpr, &intervalMin); err != nil {
			return created, deleted, err
		}
		existing[id] = true
		handle := sc.GetHandle(ctx, ScheduledReportScheduleID(id))
		_, descErr := handle.Describe(ctx)
		exists := descErr == nil
		switch {
		case enabled && !exists:
			if err := CreateScheduledReportSchedule(ctx, tc, taskQueue, id, tenantID, cronExpr, intervalMin); err != nil {
				return created, deleted, err
			}
			created++
		case !enabled && exists:
			if err := DeleteScheduledReportSchedule(ctx, tc, id); err != nil {
				return created, deleted, err
			}
			deleted++
		}
	}
	if err := rows.Err(); err != nil {
		return created, deleted, err
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
