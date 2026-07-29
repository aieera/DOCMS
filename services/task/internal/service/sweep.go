// Task 6 (2026-07-28 task-service design) — the hourly notification
// sweep. Ported from the document service's legacy tasks.go:337-390
// (SweepTaskNotifications/sweepOne/listAllTenantsForSweep), with the
// recipient sets widened for this service's multi-assignee model:
// due-soon notifies every current assignee (the legacy single-assignee
// version notified exactly one); overdue notifies assignees ∪ creator,
// deduped (the legacy version did the same two-recipient union, just
// with at most one assignee instead of many). The claim primitives
// themselves (Tasks.ClaimDueSoon/ClaimOverdue) are unchanged — see
// repository/tasks_repo.go's doc comments.
package service

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/services/task/internal/model"
)

// SweepTaskNotifications scans every tenant for tasks crossing the
// due-soon (due within 24h) or overdue thresholds and emits one notify
// event per claimed row. Called by the hourly ticker wired in
// cmd/server/main.go (once on boot, then every hour). A per-tenant
// failure is swallowed so one tenant's bad data can't block every other
// tenant's sweep that tick — ported verbatim from the document service's
// sweepOne call site, which does the same.
func (s *TaskService) SweepTaskNotifications(ctx context.Context) error {
	now := time.Now().UTC()
	tenants, err := s.listAllTenantsForSweep(ctx)
	if err != nil {
		return err
	}
	for _, tenantID := range tenants {
		_ = s.sweepOne(ctx, tenantID, now)
	}
	return nil
}

// sweepOne claims due-soon and overdue tasks for one tenant inside a
// single tenant tx, emitting one notify per claimed row. The repo's
// Claim* methods use UPDATE...RETURNING to atomically flip the relevant
// single-shot flag (reminded_at / overdue_notified_at) and hand back
// only the rows that just transitioned, so a crash between the claim and
// the notify emit rolls the whole tx back (the flag flip is undone too)
// rather than leaving a task claimed-but-never-notified — the next tick
// will pick it up again. Conversely, once this tx commits, a task can
// never be claimed (and therefore never notified) twice for the same
// threshold.
func (s *TaskService) sweepOne(ctx context.Context, tenantID uuid.UUID, now time.Time) error {
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		dueSoon, err := s.Repos.Tasks.ClaimDueSoon(ctx, tx, now)
		if err != nil {
			return err
		}
		for i := range dueSoon {
			if err := s.emitDueSoonNotify(ctx, tx, tenantID, dueSoon[i].ID); err != nil {
				return err
			}
		}

		overdue, err := s.Repos.Tasks.ClaimOverdue(ctx, tx, now)
		if err != nil {
			return err
		}
		for i := range overdue {
			if err := s.emitOverdueNotify(ctx, tx, tenantID, overdue[i].ID); err != nil {
				return err
			}
		}
		return nil
	})
}

// listAllTenantsForSweep reads every organization id outside any tenant
// tx — acceptable because, per the document service's original comment,
// `organizations` carries no per-tenant RLS policy that depends on
// current_setting('app.current_tenant'). Ported verbatim (query and
// rationale) from the document service's helper of the same name.
func (s *TaskService) listAllTenantsForSweep(ctx context.Context) ([]uuid.UUID, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id FROM organizations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// emitDueSoonNotify hydrates taskID's current assignees (Tasks.Claim*
// returns bare task rows — no Assignees/Documents side-table join, see
// repository/tasks_repo.go's scanTaskRows) and notifies all of them. A
// task with zero assignees emits nothing (emitNotify no-ops on an empty
// recipient list) — there's no one to remind.
func (s *TaskService) emitDueSoonNotify(ctx context.Context, tx pgx.Tx, tenantID, taskID uuid.UUID) error {
	t, err := s.Repos.Tasks.GetByID(ctx, tx, tenantID, taskID)
	if err != nil {
		return err
	}
	recipients := make([]uuid.UUID, len(t.Assignees))
	for i, a := range t.Assignees {
		recipients[i] = a.UserID
	}
	due := ""
	if t.DueAt != nil {
		due = t.DueAt.Format(time.RFC3339)
	}
	return s.emitNotify(ctx, tx, tenantID, taskID, "due_soon", "Task due within 24 hours", t.Title+" — due "+due, recipients)
}

// emitOverdueNotify hydrates taskID and notifies assignees ∪ creator,
// deduped — unlike due-soon, a task with zero assignees still pings the
// creator (there's always at least one stakeholder: whoever made it).
func (s *TaskService) emitOverdueNotify(ctx context.Context, tx pgx.Tx, tenantID, taskID uuid.UUID) error {
	t, err := s.Repos.Tasks.GetByID(ctx, tx, tenantID, taskID)
	if err != nil {
		return err
	}
	return s.emitNotify(ctx, tx, tenantID, taskID, "overdue", "Task is overdue", t.Title, assigneesAndCreator(t))
}

// assigneesAndCreator returns t's current assignees plus its creator,
// deduped (order: creator first, then assignees in their stored order).
func assigneesAndCreator(t *model.Task) []uuid.UUID {
	seen := make(map[uuid.UUID]bool, len(t.Assignees)+1)
	out := make([]uuid.UUID, 0, len(t.Assignees)+1)
	add := func(id uuid.UUID) {
		if seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	add(t.CreatedBy)
	for _, a := range t.Assignees {
		add(a.UserID)
	}
	return out
}
