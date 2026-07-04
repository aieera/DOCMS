// Saved-report CRUD (ADR 0119). RLS-forced table; all access via
// WithTenantTx.
package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/services/document/internal/model"
)

type reportRepo struct{}

const reportCols = `tenant_id, id, name, description, query, chart_type,
	schedule_enabled, schedule_cron, schedule_interval_minutes, channels,
	created_by, created_at, updated_at, last_run_at`

func scanReport(row pgx.Row) (*model.SavedReport, error) {
	r := &model.SavedReport{}
	err := row.Scan(&r.TenantID, &r.ID, &r.Name, &r.Description, &r.Query, &r.ChartType,
		&r.ScheduleEnabled, &r.ScheduleCron, &r.ScheduleIntervalMinutes, &r.Channels,
		&r.CreatedBy, &r.CreatedAt, &r.UpdatedAt, &r.LastRunAt)
	if err != nil {
		return nil, mapPgError(err)
	}
	return r, nil
}

func (r *reportRepo) Create(ctx context.Context, tx pgx.Tx, s *model.SavedReport) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO saved_reports (tenant_id, id, name, description, query, chart_type,
			schedule_enabled, schedule_cron, schedule_interval_minutes, channels, created_by, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
	`, s.TenantID, s.ID, s.Name, s.Description, s.Query, s.ChartType,
		s.ScheduleEnabled, s.ScheduleCron, s.ScheduleIntervalMinutes, s.Channels,
		s.CreatedBy, s.CreatedAt, s.UpdatedAt)
	return mapPgError(err)
}

func (r *reportRepo) Update(ctx context.Context, tx pgx.Tx, s *model.SavedReport) (bool, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE saved_reports
		   SET name=$3, description=$4, query=$5, chart_type=$6,
		       schedule_enabled=$7, schedule_cron=$8, schedule_interval_minutes=$9,
		       channels=$10, updated_at=$11
		 WHERE tenant_id=$1 AND id=$2
	`, s.TenantID, s.ID, s.Name, s.Description, s.Query, s.ChartType,
		s.ScheduleEnabled, s.ScheduleCron, s.ScheduleIntervalMinutes, s.Channels, time.Now().UTC())
	if err != nil {
		return false, mapPgError(err)
	}
	return tag.RowsAffected() > 0, nil
}

func (r *reportRepo) GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.SavedReport, error) {
	return scanReport(tx.QueryRow(ctx,
		`SELECT `+reportCols+` FROM saved_reports WHERE tenant_id=$1 AND id=$2`, tenantID, id))
}

func (r *reportRepo) List(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) ([]model.SavedReport, error) {
	rows, err := tx.Query(ctx,
		`SELECT `+reportCols+` FROM saved_reports WHERE tenant_id=$1 ORDER BY name, id`, tenantID)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	var out []model.SavedReport
	for rows.Next() {
		s, err := scanReport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, mapPgError(rows.Err())
}

func (r *reportRepo) Delete(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (bool, error) {
	tag, err := tx.Exec(ctx,
		`DELETE FROM saved_reports WHERE tenant_id=$1 AND id=$2`, tenantID, id)
	if err != nil {
		return false, mapPgError(err)
	}
	return tag.RowsAffected() > 0, nil
}

func (r *reportRepo) TouchLastRun(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error {
	_, err := tx.Exec(ctx,
		`UPDATE saved_reports SET last_run_at=now() WHERE tenant_id=$1 AND id=$2`, tenantID, id)
	return mapPgError(err)
}
