// ADR 0119 — scheduled-report workflow: one activity per tick.
// Driven by a Temporal Schedule per enabled report (see
// scheduled_report_schedule.go); all the work happens DB-side in the
// DeliverScheduledReport activity, so the workflow body stays trivially
// deterministic.
package workflows

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/aieera/sedoc/services/workflow/internal/activities"
)

// ScheduledReportInput identifies the report to run.
type ScheduledReportInput struct {
	ReportID string `json:"report_id"`
	TenantID string `json:"tenant_id"`
}

// ScheduledReportWorkflow executes one scheduled run of a saved report.
func ScheduledReportWorkflow(ctx workflow.Context, in ScheduledReportInput) (*activities.DeliverReportOutput, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 2 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 3,
		},
	})
	var out activities.DeliverReportOutput
	if err := workflow.ExecuteActivity(ctx, "DeliverScheduledReport",
		activities.DeliverReportInput{ReportID: in.ReportID, TenantID: in.TenantID}).Get(ctx, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
