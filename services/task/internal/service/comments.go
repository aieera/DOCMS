// Task 6 (2026-07-28 task-service design) — comments with @mentions and
// the activity feed reader, plus the notification sweep's shared
// mention-body helpers. Comment CRUD lives here; ListActivity (a thin
// read over the append-only task_activity table every other file in this
// package already writes to) rides along since it shares no state with
// sweep.go. The hourly sweep itself is sweep.go.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/services/task/internal/model"
	"github.com/aieera/sedoc/services/task/internal/repository"
)

// mentionRE matches `@[Display Name](uuid)` — the wire format
// web/src/api/comments.ts's mentionToken helper produces
// (`@[${displayName}](${userId})`, comments.ts:78) and the exact
// convention the document service's comment mentions already use
// (services/document/internal/service/comments.go:34's mentionRE).
//
// The character class here is case-insensitive hex ([0-9a-fA-F-]),
// matching the document service's regex semantics. The task-6 brief's
// literal regex text used a lowercase-only class ([0-9a-f-]); since
// every match is re-validated with uuid.Parse below regardless (itself
// case-insensitive), narrowing to lowercase-only would just mean
// silently ignoring a well-formed-but-uppercase token instead of parsing
// it — matching the document service's proven behavior over inventing a
// new edge case.
var mentionRE = regexp.MustCompile(`@\[[^\]]*\]\(([0-9a-fA-F-]{36})\)`)

// parseMentions extracts the deduped set of user ids embedded in body as
// @[Display Name](uuid) tokens, preserving first-occurrence order. A
// token that matches the bracket/paren shape but whose captured group
// isn't a valid UUID (wrong length, dashes in the wrong place, etc.) is
// silently dropped, not treated as an error — same "best effort"
// contract the document service's parser uses.
//
// Pure — no ctx, no DB. AddComment/UpdateComment below still validate
// the extracted ids against `users` before persisting/notifying on them;
// this function only answers "what does the body's markup claim".
func parseMentions(body string) []uuid.UUID {
	seen := make(map[uuid.UUID]bool)
	var out []uuid.UUID
	for _, m := range mentionRE.FindAllStringSubmatch(body, -1) {
		id, err := uuid.Parse(m[1])
		if err != nil {
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// mentionNotifyBodyMaxLen caps the body excerpt carried in a
// dms.notify.task.mention.v1 payload. 200 bytes (plus a trailing "…"
// when actually truncated) matches the document service's comment-
// mention notify excerpt (services/document/internal/service/
// comments.go's emitMention) — long enough to give the recipient real
// context, short enough that a near-4000-char task comment doesn't bloat
// the notify payload.
const mentionNotifyBodyMaxLen = 200

// AddComment posts a new comment on taskID. Any authenticated tenant
// member may comment — unlike edit/delete, there is no participant gate.
// taskID must resolve to a live (non-soft-deleted) task or this is
// ErrNotFound.
//
// Mentions embedded in the body are parsed, then filtered down to ids
// that resolve to a live user in this tenant via one batch query
// (unknown ids are silently dropped, not rejected — a stale or mistyped
// @mention shouldn't fail the whole comment write, mirroring
// validateAssigneesExist's query shape in tasks.go but inverting its
// error-on-miss behavior). The surviving ids are persisted in the
// comment's `mentions` column as-is, even when one of them is the
// author's own id — but the dms.notify.task.mention.v1 fan-out excludes
// the author: self-mentioning doesn't need a ping.
func (s *TaskService) AddComment(ctx context.Context, taskID uuid.UUID, body string) (*model.TaskComment, error) {
	tenantID, userID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}

	body = strings.TrimSpace(body)
	if body == "" {
		return nil, validationErr("body is required")
	}
	if len(body) > 4000 {
		return nil, validationErr("body must be at most 4000 characters")
	}

	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("generate comment id: %w", err)
	}
	now := time.Now().UTC()
	c := &model.TaskComment{
		TenantID: tenantID, ID: id, TaskID: taskID, AuthorID: userID,
		Body: body, CreatedAt: now, UpdatedAt: now,
	}

	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if _, err := s.Repos.Tasks.GetByID(ctx, tx, tenantID, taskID); err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return notFoundErr("task not found")
			}
			return err
		}

		mentions, err := filterExistingUserIDs(ctx, tx, tenantID, parseMentions(body))
		if err != nil {
			return err
		}
		c.Mentions = mentions

		if err := s.Repos.Comments.Create(ctx, tx, c); err != nil {
			return err
		}

		detail, err := json.Marshal(map[string]any{"comment_id": id.String()})
		if err != nil {
			return fmt.Errorf("marshal commented activity detail: %w", err)
		}
		if err := s.Repos.Activity.Insert(ctx, tx, tenantID, &model.TaskActivity{
			TaskID: taskID, ActorID: userID, Action: "commented", Detail: detail,
		}); err != nil {
			return err
		}

		if err := s.emitTaskEvent(ctx, tx, tenantID, taskID, "dms.task.comment.created.v1", map[string]any{
			"task_id":    taskID.String(),
			"comment_id": id.String(),
			"author_id":  userID.String(),
			"mentions":   uuidsToStrings(mentions),
		}); err != nil {
			return err
		}

		notifyTargets := excludeUUID(mentions, userID)
		return s.emitNotify(ctx, tx, tenantID, taskID, "mention", "You were mentioned on a task",
			truncateForNotify(body, mentionNotifyBodyMaxLen), notifyTargets)
	})
	if err != nil {
		return nil, err
	}
	return c, nil
}

// ListComments returns taskID's non-deleted comments oldest-first (the
// repository's ORDER BY created_at ASC). Any tenant member may list — no
// per-row gate, same visibility policy as GetTask.
func (s *TaskService) ListComments(ctx context.Context, taskID uuid.UUID, limit, offset int) ([]model.TaskComment, error) {
	tenantID, _, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	limit, offset = normalizeLimitOffset(limit, offset)

	var out []model.TaskComment
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var err error
		out, err = s.Repos.Comments.List(ctx, tx, tenantID, taskID, limit, offset)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateComment edits a comment's body. Author-only (ErrForbidden
// otherwise) — even an admin must go through DeleteComment to take a
// comment down, not rewrite it. Re-parses and re-validates mentions the
// same way AddComment does and refreshes the `mentions` column, but —
// per the brief — sends no new dms.notify.task.mention.v1 (re-pinging
// mentioned users on every typo fix is annoying) and records no activity
// row (an edit isn't its own feed event the way posting/deleting is).
func (s *TaskService) UpdateComment(ctx context.Context, taskID, commentID uuid.UUID, body string) (*model.TaskComment, error) {
	tenantID, userID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, validationErr("body is required")
	}
	if len(body) > 4000 {
		return nil, validationErr("body must be at most 4000 characters")
	}

	var updated *model.TaskComment
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		existing, err := s.Repos.Comments.GetByID(ctx, tx, tenantID, commentID)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return notFoundErr("comment not found")
			}
			return err
		}
		if existing.TaskID != taskID {
			// A commentID that resolves but belongs to a different task
			// looks, from this route's perspective, exactly like "no such
			// comment on this task" — not a permission question.
			return notFoundErr("comment not found")
		}
		if existing.AuthorID != userID {
			return forbiddenErr("only the author may edit a comment")
		}

		mentions, err := filterExistingUserIDs(ctx, tx, tenantID, parseMentions(body))
		if err != nil {
			return err
		}
		existing.Body = body
		existing.Mentions = mentions
		existing.UpdatedAt = time.Now().UTC()

		if err := s.Repos.Comments.Update(ctx, tx, existing); err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return notFoundErr("comment not found")
			}
			return err
		}
		updated = existing
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// DeleteComment soft-deletes a comment. Author OR admin/owner. No
// activity row, no event — matches UpdateComment's "editing/removing a
// comment isn't its own feed event" contract.
func (s *TaskService) DeleteComment(ctx context.Context, taskID, commentID uuid.UUID) error {
	tenantID, userID, role, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		existing, err := s.Repos.Comments.GetByID(ctx, tx, tenantID, commentID)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return notFoundErr("comment not found")
			}
			return err
		}
		if existing.TaskID != taskID {
			return notFoundErr("comment not found")
		}
		if existing.AuthorID != userID && !isAdmin(role) {
			return forbiddenErr("only the author or an admin may delete a comment")
		}
		if err := s.Repos.Comments.SoftDelete(ctx, tx, tenantID, commentID); err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return notFoundErr("comment not found")
			}
			return err
		}
		return nil
	})
}

// ListActivity returns taskID's activity feed newest-first (the
// repository's ORDER BY id DESC). Any tenant member may list — same
// visibility policy as ListComments/GetTask.
func (s *TaskService) ListActivity(ctx context.Context, taskID uuid.UUID, limit, offset int) ([]model.TaskActivity, error) {
	tenantID, _, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	limit, offset = normalizeLimitOffset(limit, offset)

	var out []model.TaskActivity
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var err error
		out, err = s.Repos.Activity.List(ctx, tx, tenantID, taskID, limit, offset)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ---- tx-scoped validation helpers -----------------------------------------

// filterExistingUserIDs returns the subset of ids that resolve to a live
// user row for tenantID, via one SELECT ... = ANY($2) query — same query
// shape as tasks.go's validateAssigneesExist, but returning the
// surviving subset instead of erroring on any miss.
func filterExistingUserIDs(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, ids []uuid.UUID) ([]uuid.UUID, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT id FROM users WHERE tenant_id = $1 AND id = ANY($2)`, tenantID, ids)
	if err != nil {
		return nil, fmt.Errorf("filter mention ids: %w", err)
	}
	defer rows.Close()

	found := make(map[uuid.UUID]bool, len(ids))
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("filter mention ids: %w", err)
		}
		found[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("filter mention ids: %w", err)
	}

	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if found[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

// ---- small pure helpers --------------------------------------------------

// excludeUUID returns ids with every occurrence of exclude removed,
// preserving order.
func excludeUUID(ids []uuid.UUID, exclude uuid.UUID) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if id == exclude {
			continue
		}
		out = append(out, id)
	}
	return out
}

// truncateForNotify shortens s to at most max bytes, appending an
// ellipsis when it actually cut something. Byte-based (not rune-aware),
// mirroring the document service's emitMention excerpt logic exactly.
func truncateForNotify(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
