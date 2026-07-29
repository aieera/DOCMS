// Task 5 (2026-07-28 task-service design) — assignee and document link
// management: AddAssignee/RemoveAssignee/LinkDocument/UnlinkDocument.
// Every method reads the task first (to gate + decide idempotency),
// mutates, records its activity/event, and returns a fresh GetByID
// inside the same tx.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/services/task/internal/model"
	"github.com/aieera/sedoc/services/task/internal/repository"
)

// AddAssignee adds userID to taskID's assignees. Gated by canManageLinks
// (creator, any current assignee, or admin — same gate as canTransition).
// Idempotent: re-adding an already-current assignee is a no-op (no
// duplicate activity row, event, or notify) since the repo's AddAssignee
// itself is an ON CONFLICT DO NOTHING upsert and wouldn't tell us whether
// anything actually changed.
func (s *TaskService) AddAssignee(ctx context.Context, taskID, userID uuid.UUID) (*model.Task, error) {
	tenantID, actorID, role, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}

	var result *model.Task
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		cur, err := s.Repos.Tasks.GetByID(ctx, tx, tenantID, taskID)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return notFoundErr("task not found")
			}
			return err
		}
		if !canManageLinks(cur, actorID, role) {
			return forbiddenErr("only the creator, an assignee, or an admin may manage a task's assignees")
		}
		if err := validateAssigneesExist(ctx, tx, tenantID, []uuid.UUID{userID}); err != nil {
			return err
		}

		alreadyAssignee := isAssignee(cur, userID)
		if err := s.Repos.Tasks.AddAssignee(ctx, tx, tenantID, taskID, userID, actorID); err != nil {
			return err
		}

		if !alreadyAssignee {
			detail, err := json.Marshal(map[string]any{"user_id": userID.String()})
			if err != nil {
				return fmt.Errorf("marshal assigned activity detail: %w", err)
			}
			if err := s.Repos.Activity.Insert(ctx, tx, tenantID, &model.TaskActivity{
				TaskID: taskID, ActorID: actorID, Action: "assigned", Detail: detail,
			}); err != nil {
				return err
			}
			if err := s.emitTaskEvent(ctx, tx, tenantID, taskID, "dms.task.assigned.v1", map[string]any{
				"task_id":     taskID.String(),
				"user_id":     userID.String(),
				"assigned_by": actorID.String(),
			}); err != nil {
				return err
			}
			if userID != actorID {
				if err := s.emitNotify(ctx, tx, tenantID, taskID, "assigned", "A task was assigned to you", cur.Title, []uuid.UUID{userID}); err != nil {
					return err
				}
			}
		}

		refreshed, err := s.Repos.Tasks.GetByID(ctx, tx, tenantID, taskID)
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

// RemoveAssignee removes userID from taskID's assignees. Gated by
// canManageLinks OR userID == actor — self-removal is always allowed,
// even for a plain assignee who couldn't otherwise manage links (they
// can still take themselves off a task). Removing someone who isn't
// currently assigned is a validation error, not a silent no-op.
func (s *TaskService) RemoveAssignee(ctx context.Context, taskID, userID uuid.UUID) (*model.Task, error) {
	tenantID, actorID, role, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}

	var result *model.Task
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		cur, err := s.Repos.Tasks.GetByID(ctx, tx, tenantID, taskID)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return notFoundErr("task not found")
			}
			return err
		}
		if !canManageLinks(cur, actorID, role) && userID != actorID {
			return forbiddenErr("only the creator, an assignee, an admin, or the assignee themselves may remove an assignee")
		}
		if !isAssignee(cur, userID) {
			return validationErr("user is not an assignee of this task")
		}

		removed, err := s.Repos.Tasks.RemoveAssignee(ctx, tx, tenantID, taskID, userID)
		if err != nil {
			return err
		}
		if removed {
			detail, err := json.Marshal(map[string]any{"user_id": userID.String()})
			if err != nil {
				return fmt.Errorf("marshal unassigned activity detail: %w", err)
			}
			if err := s.Repos.Activity.Insert(ctx, tx, tenantID, &model.TaskActivity{
				TaskID: taskID, ActorID: actorID, Action: "unassigned", Detail: detail,
			}); err != nil {
				return err
			}
			if err := s.emitTaskEvent(ctx, tx, tenantID, taskID, "dms.task.unassigned.v1", map[string]any{
				"task_id":       taskID.String(),
				"user_id":       userID.String(),
				"unassigned_by": actorID.String(),
			}); err != nil {
				return err
			}
		}

		refreshed, err := s.Repos.Tasks.GetByID(ctx, tx, tenantID, taskID)
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

// LinkDocument links documentID onto taskID. Gated by canManageLinks; an
// unknown or cross-tenant/deleted document id is a validation error (the
// repository resolves it against `documents` and returns ErrNotFound,
// which we remap). Idempotent: relinking an already-linked document
// still refreshes its workspace/title snapshot at the repo layer (ON
// CONFLICT DO UPDATE) but records no duplicate activity/event.
func (s *TaskService) LinkDocument(ctx context.Context, taskID, documentID uuid.UUID) (*model.Task, error) {
	tenantID, actorID, role, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}

	var result *model.Task
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		cur, err := s.Repos.Tasks.GetByID(ctx, tx, tenantID, taskID)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return notFoundErr("task not found")
			}
			return err
		}
		if !canManageLinks(cur, actorID, role) {
			return forbiddenErr("only the creator, an assignee, or an admin may manage a task's linked documents")
		}

		alreadyLinked := isLinkedDocument(cur, documentID)
		d, err := s.Repos.Tasks.LinkDocument(ctx, tx, tenantID, taskID, documentID, actorID)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return validationErr(fmt.Sprintf("document %s not found", documentID))
			}
			return err
		}

		if !alreadyLinked {
			detail, err := json.Marshal(map[string]any{"document_id": documentID.String(), "title": d.TitleSnapshot})
			if err != nil {
				return fmt.Errorf("marshal document_linked activity detail: %w", err)
			}
			if err := s.Repos.Activity.Insert(ctx, tx, tenantID, &model.TaskActivity{
				TaskID: taskID, ActorID: actorID, Action: "document_linked", Detail: detail,
			}); err != nil {
				return err
			}
			if err := s.emitTaskEvent(ctx, tx, tenantID, taskID, "dms.task.document_linked.v1", map[string]any{
				"task_id":     taskID.String(),
				"document_id": documentID.String(),
				"title":       d.TitleSnapshot,
				"linked_by":   actorID.String(),
			}); err != nil {
				return err
			}
		}

		refreshed, err := s.Repos.Tasks.GetByID(ctx, tx, tenantID, taskID)
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

// UnlinkDocument removes documentID from taskID's linked documents.
// Gated by canManageLinks; unlinking a document that isn't currently
// linked is a validation error, mirroring RemoveAssignee.
func (s *TaskService) UnlinkDocument(ctx context.Context, taskID, documentID uuid.UUID) (*model.Task, error) {
	tenantID, actorID, role, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}

	var result *model.Task
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		cur, err := s.Repos.Tasks.GetByID(ctx, tx, tenantID, taskID)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return notFoundErr("task not found")
			}
			return err
		}
		if !canManageLinks(cur, actorID, role) {
			return forbiddenErr("only the creator, an assignee, or an admin may manage a task's linked documents")
		}

		title := documentTitleSnapshot(cur, documentID)
		if title == nil {
			return validationErr("document is not linked to this task")
		}

		removed, err := s.Repos.Tasks.UnlinkDocument(ctx, tx, tenantID, taskID, documentID)
		if err != nil {
			return err
		}
		if removed {
			detail, err := json.Marshal(map[string]any{"document_id": documentID.String(), "title": *title})
			if err != nil {
				return fmt.Errorf("marshal document_unlinked activity detail: %w", err)
			}
			if err := s.Repos.Activity.Insert(ctx, tx, tenantID, &model.TaskActivity{
				TaskID: taskID, ActorID: actorID, Action: "document_unlinked", Detail: detail,
			}); err != nil {
				return err
			}
			if err := s.emitTaskEvent(ctx, tx, tenantID, taskID, "dms.task.document_unlinked.v1", map[string]any{
				"task_id":     taskID.String(),
				"document_id": documentID.String(),
				"title":       *title,
				"unlinked_by": actorID.String(),
			}); err != nil {
				return err
			}
		}

		refreshed, err := s.Repos.Tasks.GetByID(ctx, tx, tenantID, taskID)
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

// isLinkedDocument reports whether documentID is currently linked to t.
func isLinkedDocument(t *model.Task, documentID uuid.UUID) bool {
	for _, d := range t.Documents {
		if d.DocumentID == documentID {
			return true
		}
	}
	return false
}

// documentTitleSnapshot returns the linked title snapshot for
// documentID on t, or nil if it isn't currently linked.
func documentTitleSnapshot(t *model.Task, documentID uuid.UUID) *string {
	for _, d := range t.Documents {
		if d.DocumentID == documentID {
			title := d.TitleSnapshot
			return &title
		}
	}
	return nil
}
