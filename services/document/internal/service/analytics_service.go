// Analytics query API + saved reports (ADR 0119).
//
// RunAnalyticsQuery executes a governed analytics.Query: names resolve
// against the dataset registry, values bind as parameters, the tenant
// predicate is compiled in, AND execution happens inside WithTenantTx
// so RLS is the second lock. The whole surface is admin/owner-gated —
// aggregate analytics reveal tenant-wide facts (per-workspace counts,
// per-user activity) that ordinary members aren't entitled to.
package service

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/pkg/analytics"
	"github.com/aieera/sedoc/pkg/auth"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/model"
)

func requireAnalyticsRole(ctx context.Context) error {
	switch auth.GetUserRole(ctx) {
	case "admin", "owner":
		return nil
	default:
		return vdmserr.Forbidden("analytics requires the admin or owner role")
	}
}

// QueryResult is the tabular response: column names + row values in
// column order (dimensions first, then measures).
type QueryResult struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
}

func (s *DocumentService) RunAnalyticsQuery(ctx context.Context, q analytics.Query) (*QueryResult, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireAnalyticsRole(ctx); err != nil {
		return nil, err
	}
	compiled, err := analytics.Compile(q, tenantID)
	if err != nil {
		return nil, vdmserr.Validation("query", err.Error())
	}
	res := &QueryResult{Columns: compiled.Columns, Rows: [][]any{}}
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, compiled.SQL, compiled.Args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			vals, err := rows.Values()
			if err != nil {
				return err
			}
			res.Rows = append(res.Rows, normalizeRow(vals))
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// normalizeRow converts pgx-native values into JSON-friendly ones.
func normalizeRow(vals []any) []any {
	out := make([]any, len(vals))
	for i, v := range vals {
		switch t := v.(type) {
		case time.Time:
			out[i] = t.UTC().Format(time.RFC3339)
		case [16]byte: // uuid raw
			out[i] = uuid.UUID(t).String()
		default:
			out[i] = v
		}
	}
	return out
}

// ---- saved reports -----------------------------------------------------

type SavedReportInput struct {
	Name                    string
	Description             string
	Query                   json.RawMessage
	ChartType               string
	ScheduleEnabled         bool
	ScheduleCron            string
	ScheduleIntervalMinutes int
	Channels                []string
}

func validReportChannels(chs []string) error {
	for _, c := range chs {
		switch c {
		case "in_app", "email", "digest":
		default:
			return vdmserr.Validation("channels", "must be in_app, email or digest")
		}
	}
	return nil
}

func (s *DocumentService) validateReportInput(in *SavedReportInput) (*analytics.Query, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, errInvalidInput("name", "required")
	}
	var q analytics.Query
	if err := json.Unmarshal(in.Query, &q); err != nil {
		return nil, vdmserr.Validation("query", "invalid json: "+err.Error())
	}
	// Compile against a placeholder tenant purely for validation.
	if _, err := analytics.Compile(q, uuid.Nil); err != nil {
		return nil, vdmserr.Validation("query", err.Error())
	}
	switch in.ChartType {
	case "", "table", "bar", "line", "pie":
	default:
		return nil, vdmserr.Validation("chart_type", "must be table, bar, line or pie")
	}
	if in.ChartType == "" {
		in.ChartType = "table"
	}
	if len(in.Channels) == 0 {
		in.Channels = []string{"in_app"}
	}
	if err := validReportChannels(in.Channels); err != nil {
		return nil, err
	}
	if in.ScheduleEnabled && in.ScheduleCron == "" && in.ScheduleIntervalMinutes <= 0 {
		return nil, vdmserr.Validation("schedule", "enabled schedule needs a cron or interval")
	}
	if in.ScheduleIntervalMinutes < 0 {
		return nil, vdmserr.Validation("schedule_interval_minutes", "must be >= 0")
	}
	return &q, nil
}

func (s *DocumentService) CreateSavedReport(ctx context.Context, in SavedReportInput) (*model.SavedReport, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireAnalyticsRole(ctx); err != nil {
		return nil, err
	}
	if _, err := s.validateReportInput(&in); err != nil {
		return nil, err
	}
	id, err := newExternalID()
	if err != nil {
		return nil, err
	}
	r := &model.SavedReport{
		TenantID: tenantID, ID: id,
		Name: strings.TrimSpace(in.Name), Description: in.Description,
		Query: in.Query, ChartType: in.ChartType,
		ScheduleEnabled: in.ScheduleEnabled, ScheduleCron: in.ScheduleCron,
		ScheduleIntervalMinutes: in.ScheduleIntervalMinutes, Channels: in.Channels,
		CreatedBy: userID, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return s.repos.Reports.Create(ctx, tx, r)
	})
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (s *DocumentService) UpdateSavedReport(ctx context.Context, id uuid.UUID, in SavedReportInput) (*model.SavedReport, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireAnalyticsRole(ctx); err != nil {
		return nil, err
	}
	if _, err := s.validateReportInput(&in); err != nil {
		return nil, err
	}
	var out *model.SavedReport
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		r := &model.SavedReport{
			TenantID: tenantID, ID: id,
			Name: strings.TrimSpace(in.Name), Description: in.Description,
			Query: in.Query, ChartType: in.ChartType,
			ScheduleEnabled: in.ScheduleEnabled, ScheduleCron: in.ScheduleCron,
			ScheduleIntervalMinutes: in.ScheduleIntervalMinutes, Channels: in.Channels,
		}
		ok, err := s.repos.Reports.Update(ctx, tx, r)
		if err != nil {
			return err
		}
		if !ok {
			return vdmserr.NotFound("report not found")
		}
		out, err = s.repos.Reports.GetByID(ctx, tx, tenantID, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *DocumentService) GetSavedReport(ctx context.Context, id uuid.UUID) (*model.SavedReport, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireAnalyticsRole(ctx); err != nil {
		return nil, err
	}
	var out *model.SavedReport
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		out, err = s.repos.Reports.GetByID(ctx, tx, tenantID, id)
		return err
	})
	return out, err
}

func (s *DocumentService) ListSavedReports(ctx context.Context) ([]model.SavedReport, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireAnalyticsRole(ctx); err != nil {
		return nil, err
	}
	var out []model.SavedReport
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		out, err = s.repos.Reports.List(ctx, tx, tenantID)
		return err
	})
	return out, err
}

func (s *DocumentService) DeleteSavedReport(ctx context.Context, id uuid.UUID) error {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	if err := requireAnalyticsRole(ctx); err != nil {
		return err
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		ok, err := s.repos.Reports.Delete(ctx, tx, tenantID, id)
		if err != nil {
			return err
		}
		if !ok {
			return vdmserr.NotFound("report not found")
		}
		return nil
	})
}

// RunSavedReport executes a saved report's stored query and stamps
// last_run_at (manual runs count as runs — previously only scheduled
// deliveries touched it; review finding).
func (s *DocumentService) RunSavedReport(ctx context.Context, id uuid.UUID) (*QueryResult, *model.SavedReport, error) {
	r, err := s.GetSavedReport(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	var q analytics.Query
	if err := json.Unmarshal(r.Query, &q); err != nil {
		return nil, nil, vdmserr.Validation("query", "stored query invalid: "+err.Error())
	}
	res, err := s.RunAnalyticsQuery(ctx, q)
	if err != nil {
		return nil, nil, err
	}
	if terr := s.withTenantTx(ctx, r.TenantID, func(tx pgx.Tx) error {
		return s.repos.Reports.TouchLastRun(ctx, tx, r.TenantID, id)
	}); terr != nil {
		// Best-effort — the run itself succeeded.
		s.log.Warn().Err(terr).Str("report_id", id.String()).Msg("touch last_run_at failed")
	}
	return res, r, nil
}
