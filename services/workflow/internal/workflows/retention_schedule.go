// Schedule bootstrap for the retention cron.
//
// Every organization gets one daily schedule that runs RetentionWorkflow
// at 03:00 UTC. Schedule ids follow `retention-<tenant_uuid>` so the
// operator can list or pause per-tenant from `temporal schedule list`.
//
// Idempotent: re-running against an existing schedule is a no-op (we
// swallow AlreadyExists). This lets us call the bootstrap on every
// worker start without accumulating duplicates.
package workflows

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
)

// ScheduleTaskQueue is the taskqueue the schedule should dispatch to.
// Default matches cmd/worker's defaultQueue; callers can override.
const ScheduleTaskQueue = "vaultdms-default"

// RegisterRetentionSchedules enumerates the organizations table and
// ensures each tenant has a retention schedule. Returns the number of
// schedules newly created.
//
// Note: we deliberately do not delete schedules for tenants that have
// been de-provisioned — the CMK scheduled-deletion workflow handles
// that path (Wave 6.1 out-of-scope). Orphaned schedules surface as
// Temporal errors when the workflow runs against a missing tenant;
// that's a louder signal than silent pruning.
func RegisterRetentionSchedules(ctx context.Context, pool *pgxpool.Pool, tc client.Client, taskQueue string) (int, error) {
	if taskQueue == "" {
		taskQueue = ScheduleTaskQueue
	}
	rows, err := pool.Query(ctx, `SELECT id::text FROM organizations WHERE deleted_at IS NULL`)
	if err != nil {
		return 0, fmt.Errorf("list tenants: %w", err)
	}
	defer rows.Close()

	var created int
	sc := tc.ScheduleClient()
	for rows.Next() {
		var tenantID string
		if err := rows.Scan(&tenantID); err != nil {
			return created, err
		}
		id := "retention-" + tenantID
		_, err := sc.Create(ctx, client.ScheduleOptions{
			ID: id,
			Spec: client.ScheduleSpec{
				CronExpressions: []string{"0 3 * * *"}, // daily 03:00 UTC
				TimeZoneName:    "UTC",
			},
			Action: &client.ScheduleWorkflowAction{
				ID:        "wf-" + id + "-" + time.Now().UTC().Format("20060102"),
				Workflow:  RetentionWorkflow,
				Args:      []any{RetentionInput{TenantID: tenantID}},
				TaskQueue: taskQueue,
			},
			Overlap: 1, // SKIP: if a previous run is still in flight, skip this tick.
		})
		if err != nil {
			// Idempotency path: AlreadyExists is fine.
			var already *serviceerror.AlreadyExists
			if errors.As(err, &already) {
				continue
			}
			// Also tolerate older Temporal server error strings.
			if strings.Contains(err.Error(), "already exists") {
				continue
			}
			return created, fmt.Errorf("create schedule %s: %w", id, err)
		}
		created++
	}
	return created, rows.Err()
}
