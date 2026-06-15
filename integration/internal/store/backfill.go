package store

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// BackfillRun is one onboarding run over existing ERP data.
type BackfillRun struct {
	ID         uuid.UUID  `json:"id"`
	Status     string     `json:"status"`
	Source     string     `json:"source"`
	SourceArg  string     `json:"source_arg"`
	Total      int        `json:"total"`
	Processed  int        `json:"processed"`
	Failed     int        `json:"failed"`
	LastError  string     `json:"last_error"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// BackfillFailure is one failed work item within a run.
type BackfillFailure struct {
	CustomerRef   string    `json:"customer_ref"`
	Item          string    `json:"item"`
	Error         string    `json:"error"`
	CorrelationID string    `json:"correlation_id"`
	CreatedAt     time.Time `json:"created_at"`
}

const backfillCols = `id, status, source, source_arg, total, processed, failed, last_error, started_at, finished_at, created_at, updated_at`

func scanBackfillRun(row pgx.Row) (*BackfillRun, error) {
	var r BackfillRun
	if err := row.Scan(&r.ID, &r.Status, &r.Source, &r.SourceArg, &r.Total, &r.Processed,
		&r.Failed, &r.LastError, &r.StartedAt, &r.FinishedAt, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	return &r, nil
}

// CreateBackfillRun inserts a run. When start is true it begins immediately
// (status=running, started_at=now) — the CLI path, which processes it in-process.
// When false it is left pending for the worker poller to claim — the BFF path.
func (s *Store) CreateBackfillRun(ctx context.Context, source, sourceArg string, start bool) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, `
		INSERT INTO backfill_runs (source, source_arg, status, started_at)
		VALUES ($1, $2,
		        CASE WHEN $3 THEN 'running' ELSE 'pending' END,
		        CASE WHEN $3 THEN now() ELSE NULL END)
		RETURNING id`, source, sourceArg, start).Scan(&id)
	return id, err
}

// ClaimPendingBackfillRun atomically transitions the oldest pending run to
// running and returns it. ok=false (nil run) when there is nothing pending.
// FOR UPDATE SKIP LOCKED so multiple workers never claim the same run.
func (s *Store) ClaimPendingBackfillRun(ctx context.Context) (*BackfillRun, bool, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE backfill_runs SET status='running', started_at=now(), updated_at=now()
		WHERE id = (
			SELECT id FROM backfill_runs WHERE status='pending'
			 ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED
		)
		RETURNING `+backfillCols)
	r, err := scanBackfillRun(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return r, true, nil
}

// SetBackfillTotal records the planned item count once enumeration is done.
func (s *Store) SetBackfillTotal(ctx context.Context, id uuid.UUID, total int) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE backfill_runs SET total=$2, updated_at=now() WHERE id=$1`, id, total)
	return err
}

// IncBackfillProgress atomically advances the processed/failed counters. Safe
// under concurrent workers (a single UPDATE per item).
func (s *Store) IncBackfillProgress(ctx context.Context, id uuid.UUID, processed, failed int) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE backfill_runs SET processed = processed + $2, failed = failed + $3, updated_at=now()
		WHERE id=$1`, id, processed, failed)
	return err
}

// RecordBackfillFailure appends a per-item failure row.
func (s *Store) RecordBackfillFailure(ctx context.Context, id uuid.UUID, customerRef, item, errMsg, correlationID string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO backfill_failures (run_id, customer_ref, item, error, correlation_id)
		VALUES ($1,$2,$3,$4,$5)`, id, customerRef, item, errMsg, correlationID)
	return err
}

// FinishBackfillRun sets the terminal status + finished_at.
func (s *Store) FinishBackfillRun(ctx context.Context, id uuid.UUID, status, lastErr string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE backfill_runs SET status=$2, last_error=$3, finished_at=now(), updated_at=now()
		WHERE id=$1`, id, status, lastErr)
	return err
}

// GetBackfillRun loads a run, or ErrNotFound.
func (s *Store) GetBackfillRun(ctx context.Context, id uuid.UUID) (*BackfillRun, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+backfillCols+` FROM backfill_runs WHERE id=$1`, id)
	r, err := scanBackfillRun(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// ListBackfillRuns returns recent runs, newest first.
func (s *Store) ListBackfillRuns(ctx context.Context, limit int) ([]BackfillRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+backfillCols+` FROM backfill_runs ORDER BY created_at DESC LIMIT `+strconv.Itoa(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BackfillRun
	for rows.Next() {
		r, err := scanBackfillRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// ListBackfillFailures returns a run's per-item failures, oldest first.
func (s *Store) ListBackfillFailures(ctx context.Context, id uuid.UUID, limit int) ([]BackfillFailure, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, `
		SELECT customer_ref, item, error, correlation_id, created_at
		FROM backfill_failures WHERE run_id=$1 ORDER BY created_at, id LIMIT `+strconv.Itoa(limit), id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BackfillFailure
	for rows.Next() {
		var f BackfillFailure
		if err := rows.Scan(&f.CustomerRef, &f.Item, &f.Error, &f.CorrelationID, &f.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
