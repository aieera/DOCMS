// ADR 0119 — scheduled-report delivery activity.
//
// Runs entirely against the shared DB (no HTTP hop): load the saved
// report, compile its governed query via pkg/analytics (the same
// compiler the document service's API uses), execute it inside the
// tenant tx, stamp last_run_at, and outbox-emit the summary event +
// the notification (with the report's channel consent) — §4.7/C5.
package activities

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/pkg/analytics"
	"github.com/aieera/sedoc/pkg/database"
)

// DeliverReportInput drives DeliverScheduledReport.
type DeliverReportInput struct {
	ReportID string `json:"report_id"`
	TenantID string `json:"tenant_id"`
}

// DeliverReportOutput is returned for workflow-history visibility.
type DeliverReportOutput struct {
	Rows int `json:"rows"`
}

// DeliverScheduledReport executes one scheduled run.
func (a *Activities) DeliverScheduledReport(ctx context.Context, in DeliverReportInput) (*DeliverReportOutput, error) {
	if a.Pool == nil || a.Outbox == nil {
		return nil, fmt.Errorf("pool/outbox not configured on activities")
	}
	tenantUUID, err := uuid.Parse(in.TenantID)
	if err != nil {
		return nil, fmt.Errorf("tenant_id: %w", err)
	}
	reportUUID, err := uuid.Parse(in.ReportID)
	if err != nil {
		return nil, fmt.Errorf("report_id: %w", err)
	}

	out := &DeliverReportOutput{}
	err = a.runTenant(ctx, in.TenantID, func(tx pgx.Tx) error {
		var (
			name      string
			queryJSON []byte
			channels  []string
			createdBy uuid.UUID
		)
		if err := tx.QueryRow(ctx, `
			SELECT name, query, channels, created_by
			  FROM saved_reports
			 WHERE tenant_id = $1 AND id = $2
		`, tenantUUID, reportUUID).Scan(&name, &queryJSON, &channels, &createdBy); err != nil {
			return fmt.Errorf("load report: %w", err)
		}

		var q analytics.Query
		if err := json.Unmarshal(queryJSON, &q); err != nil {
			return fmt.Errorf("stored query invalid: %w", err)
		}
		compiled, err := analytics.Compile(q, tenantUUID)
		if err != nil {
			return fmt.Errorf("compile: %w", err)
		}
		rows, err := tx.Query(ctx, compiled.SQL, compiled.Args...)
		if err != nil {
			return fmt.Errorf("execute: %w", err)
		}
		count := 0
		for rows.Next() {
			count++
		}
		rerr := rows.Err()
		rows.Close()
		if rerr != nil {
			return rerr
		}
		out.Rows = count

		if _, err := tx.Exec(ctx, `
			UPDATE saved_reports SET last_run_at = now()
			 WHERE tenant_id = $1 AND id = $2
		`, tenantUUID, reportUUID); err != nil {
			return err
		}

		summary, err := json.Marshal(map[string]any{
			"report_id":    in.ReportID,
			"report_name":  name,
			"rows":         count,
			"triggered_by": "schedule",
			"run_by":       createdBy.String(),
		})
		if err != nil {
			return err
		}
		evt := database.NewOutboxEvent(tenantUUID, "dms.report.generated.v1", "report", reportUUID, summary)
		if err := a.Outbox.Insert(ctx, tx, evt); err != nil {
			return err
		}

		notify, err := json.Marshal(map[string]any{
			"tenant_id":     in.TenantID,
			"user_ids":      []string{createdBy.String()},
			"type":          "report_ready",
			"title":         fmt.Sprintf("Report %q is ready", name),
			"body":          fmt.Sprintf("%d rows — open the report to view or export.", count),
			"resource_type": "report",
			"resource_id":   in.ReportID,
			"channels":      channels,
		})
		if err != nil {
			return err
		}
		return a.Outbox.Insert(ctx, tx,
			database.NewOutboxEvent(tenantUUID, "dms.notify.report_ready.v1", "report", reportUUID, notify))
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
