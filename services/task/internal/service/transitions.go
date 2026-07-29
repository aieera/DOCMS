// Task 5 (2026-07-28 task-service design) — status transitions:
// StartTask/CompleteTask/ReopenTask/CancelTask. Every method funnels
// through doTransition, which gates on canTransition, validates the
// from/to pair against the pure validTransition matrix, records one
// `status_changed` activity row + a dms.task.status_changed.v1 event,
// and — for CompleteTask only — an additional completion notify.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/services/task/internal/model"
	"github.com/aieera/sedoc/services/task/internal/repository"
)

// validTransition is the pure status-transition matrix this service
// enforces. It has no DB/ctx dependency by design so the full from/to
// grid can be unit tested in milliseconds (transitions_test.go).
//
//	open        -> in_progress, done, cancelled
//	in_progress -> done, cancelled
//	done        -> open
//	cancelled   -> open
//
// Same-state pairs and any status outside {open,in_progress,done,
// cancelled} are always illegal — a transition method moves status, it
// never no-ops, and this function doesn't know about statuses the
// `tasks.status` CHECK constraint doesn't allow.
func validTransition(from, to string) bool {
	switch from {
	case "open":
		return to == "in_progress" || to == "done" || to == "cancelled"
	case "in_progress":
		return to == "done" || to == "cancelled"
	case "done", "cancelled":
		return to == "open"
	default:
		return false
	}
}

// StartTask moves open -> in_progress.
func (s *TaskService) StartTask(ctx context.Context, id uuid.UUID) (*model.Task, error) {
	return s.doTransition(ctx, id, "in_progress", "start")
}

// CompleteTask moves open|in_progress -> done. Beyond the shared
// status_changed activity/event every transition records, Complete also
// stamps CompletedBy/CompletedAt and notifies (assignees ∪ creator) −
// actor that the task is done.
func (s *TaskService) CompleteTask(ctx context.Context, id uuid.UUID) (*model.Task, error) {
	return s.doTransition(ctx, id, "done", "complete")
}

// ReopenTask moves done|cancelled -> open, clearing CompletedBy/
// CompletedAt.
func (s *TaskService) ReopenTask(ctx context.Context, id uuid.UUID) (*model.Task, error) {
	return s.doTransition(ctx, id, "open", "reopen")
}

// CancelTask moves open|in_progress -> cancelled.
func (s *TaskService) CancelTask(ctx context.Context, id uuid.UUID) (*model.Task, error) {
	return s.doTransition(ctx, id, "cancelled", "cancel")
}

// doTransition is the shared body behind all four transition methods.
// verb is the human-readable action name ("start"/"complete"/"reopen"/
// "cancel") used only to shape the illegal-transition error message —
// the actual legality check is entirely delegated to validTransition, so
// the four wrapper methods above stay one-liners.
func (s *TaskService) doTransition(ctx context.Context, id uuid.UUID, to, verb string) (*model.Task, error) {
	tenantID, userID, role, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}

	var result *model.Task
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		cur, err := s.Repos.Tasks.GetByID(ctx, tx, tenantID, id)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return notFoundErr("task not found")
			}
			return err
		}
		if !canTransition(cur, userID, role) {
			return forbiddenErr("only the creator, an assignee, or an admin may transition a task")
		}

		from := cur.Status
		if !validTransition(from, to) {
			return validationErr(fmt.Sprintf("cannot %s a %s task: invalid transition to %s", verb, from, to))
		}

		now := time.Now().UTC()
		cur.Status = to
		switch to {
		case "done":
			cur.CompletedBy = &userID
			cur.CompletedAt = &now
		case "open":
			// Reopening from done or cancelled clears any prior completion
			// stamp — a reopened task is not "done by" anyone anymore.
			cur.CompletedBy = nil
			cur.CompletedAt = nil
		}

		if err := s.Repos.Tasks.Update(ctx, tx, cur); err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return notFoundErr("task not found")
			}
			return err
		}

		detail, err := json.Marshal(map[string]any{"from": from, "to": to})
		if err != nil {
			return fmt.Errorf("marshal status_changed activity detail: %w", err)
		}
		if err := s.Repos.Activity.Insert(ctx, tx, tenantID, &model.TaskActivity{
			TaskID: id, ActorID: userID, Action: "status_changed", Detail: detail,
		}); err != nil {
			return err
		}
		if err := s.emitTaskEvent(ctx, tx, tenantID, id, "dms.task.status_changed.v1", map[string]any{
			"task_id":    id.String(),
			"from":       from,
			"to":         to,
			"changed_by": userID.String(),
		}); err != nil {
			return err
		}

		if to == "done" {
			recipients := completionRecipients(cur, userID)
			if err := s.emitNotify(ctx, tx, tenantID, id, "completed", "Task completed", cur.Title, recipients); err != nil {
				return err
			}
		}

		// Return the post-mutation row (per the brief: every Task-5 method
		// returns a fresh GetByID inside the same tx), not the in-memory
		// cur — status transitions don't touch Assignees/Documents, but
		// this keeps the read-back contract uniform across transitions.go
		// and links.go.
		refreshed, err := s.Repos.Tasks.GetByID(ctx, tx, tenantID, id)
		if err != nil {
			return err
		}
		result = refreshed
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// completionRecipients is (t.Assignees ∪ {t.CreatedBy}) − {actor},
// deduplicated. Used by CompleteTask's "anyone completes -> notify
// everyone else with a stake in the task" fan-out.
func completionRecipients(t *model.Task, actor uuid.UUID) []uuid.UUID {
	seen := map[uuid.UUID]bool{actor: true}
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
