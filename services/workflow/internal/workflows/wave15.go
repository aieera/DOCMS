// Wave 15 Temporal workflows + schedule bootstraps.
//
// Two new daily schedules per tenant:
//   - password-expiry-<tenant>    → PasswordExpiryWorkflow    → SweepPasswordExpiries
//   - ack-reminders-<tenant>      → AckRemindersWorkflow      → SweepAcknowledgementReminders
//
// Both run at 02:00 UTC per tenant, ahead of the 03:00 retention
// sweep. Schedule IDs follow the `<kind>-<tenant>` convention so
// `temporal schedule list` shows a clean per-tenant rollup.
//
// Determinism (same as RetentionWorkflow): no time.Now, no rand,
// all I/O via activities registered on the worker.
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
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// ---- Inputs / outcomes ----------------------------------------------------

type PasswordExpiryInput struct {
	TenantID string `json:"tenant_id"`
}
type PasswordExpiryOutcome struct {
	TenantID   string `json:"tenant_id"`
	Flagged    int    `json:"flagged"`
	ExecutedAt string `json:"executed_at"`
}

type AckRemindersInput struct {
	TenantID string `json:"tenant_id"`
}
type AckRemindersOutcome struct {
	TenantID   string `json:"tenant_id"`
	Reminded   int    `json:"reminded"`
	Escalated  int    `json:"escalated"`
	ExecutedAt string `json:"executed_at"`
}

// ---- Workflows ------------------------------------------------------------

func PasswordExpiryWorkflow(ctx workflow.Context, in PasswordExpiryInput) (*PasswordExpiryOutcome, error) {
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 2 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts:    3,
			InitialInterval:    5 * time.Second,
			BackoffCoefficient: 2,
			MaximumInterval:    30 * time.Second,
		},
	}
	ctx = workflow.WithActivityOptions(ctx, ao)
	out := &PasswordExpiryOutcome{
		TenantID:   in.TenantID,
		ExecutedAt: workflow.Now(ctx).UTC().Format(time.RFC3339),
	}
	var flagged int
	if err := workflow.ExecuteActivity(ctx, "SweepPasswordExpiries", in.TenantID).
		Get(ctx, &flagged); err != nil {
		return out, err
	}
	out.Flagged = flagged
	return out, nil
}

func AckRemindersWorkflow(ctx workflow.Context, in AckRemindersInput) (*AckRemindersOutcome, error) {
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts:    3,
			InitialInterval:    10 * time.Second,
			BackoffCoefficient: 2,
			MaximumInterval:    1 * time.Minute,
		},
	}
	ctx = workflow.WithActivityOptions(ctx, ao)
	out := &AckRemindersOutcome{
		TenantID:   in.TenantID,
		ExecutedAt: workflow.Now(ctx).UTC().Format(time.RFC3339),
	}
	type sweepRes struct {
		Reminded  int
		Escalated int
	}
	var res sweepRes
	if err := workflow.ExecuteActivity(ctx, "SweepAcknowledgementReminders", in.TenantID).
		Get(ctx, &res); err != nil {
		return out, err
	}
	out.Reminded = res.Reminded
	out.Escalated = res.Escalated
	return out, nil
}

// ---- Schedule bootstrap ---------------------------------------------------

// RegisterWave15Schedules enumerates tenants and ensures each has a
// password-expiry + ack-reminder schedule. Mirrors
// RegisterRetentionSchedules' idempotency semantics: AlreadyExists
// is swallowed, non-fatal errors logged and continued. Returns the
// count of newly-created schedules across BOTH workflows.
func RegisterWave15Schedules(ctx context.Context, pool *pgxpool.Pool, tc client.Client, taskQueue string) (int, error) {
	if taskQueue == "" {
		taskQueue = ScheduleTaskQueue
	}
	rows, err := pool.Query(ctx, `SELECT id::text FROM organizations WHERE deleted_at IS NULL`)
	if err != nil {
		return 0, fmt.Errorf("list tenants: %w", err)
	}
	defer rows.Close()

	created := 0
	sc := tc.ScheduleClient()
	now := time.Now().UTC().Format("20060102")

	for rows.Next() {
		var tenantID string
		if err := rows.Scan(&tenantID); err != nil {
			return created, err
		}

		// Password expiry — daily 02:00 UTC.
		pwID := "password-expiry-" + tenantID
		_, err := sc.Create(ctx, client.ScheduleOptions{
			ID: pwID,
			Spec: client.ScheduleSpec{
				CronExpressions: []string{"0 2 * * *"},
				TimeZoneName:    "UTC",
			},
			Action: &client.ScheduleWorkflowAction{
				ID:        "wf-" + pwID + "-" + now,
				Workflow:  PasswordExpiryWorkflow,
				Args:      []any{PasswordExpiryInput{TenantID: tenantID}},
				TaskQueue: taskQueue,
			},
			Overlap: 1, // SKIP
		})
		if !scheduleErrIsBenign(err) {
			return created, fmt.Errorf("create schedule %s: %w", pwID, err)
		}
		if err == nil {
			created++
		}

		// Ack reminders — daily 09:00 UTC per the Wave 15.1 brief.
		ackID := "ack-reminders-" + tenantID
		_, err = sc.Create(ctx, client.ScheduleOptions{
			ID: ackID,
			Spec: client.ScheduleSpec{
				CronExpressions: []string{"0 9 * * *"},
				TimeZoneName:    "UTC",
			},
			Action: &client.ScheduleWorkflowAction{
				ID:        "wf-" + ackID + "-" + now,
				Workflow:  AckRemindersWorkflow,
				Args:      []any{AckRemindersInput{TenantID: tenantID}},
				TaskQueue: taskQueue,
			},
			Overlap: 1,
		})
		if !scheduleErrIsBenign(err) {
			return created, fmt.Errorf("create schedule %s: %w", ackID, err)
		}
		if err == nil {
			created++
		}
	}
	return created, rows.Err()
}

// scheduleErrIsBenign returns true when err is nil OR a Temporal
// AlreadyExists — idempotency hook for bootstrap-on-every-boot.
func scheduleErrIsBenign(err error) bool {
	if err == nil {
		return true
	}
	var already *serviceerror.AlreadyExists
	if errors.As(err, &already) {
		return true
	}
	return strings.Contains(err.Error(), "already exists")
}
