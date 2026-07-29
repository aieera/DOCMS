// Task 4 (2026-07-28 task-service design) — pure, DB-free permission
// gates for the task service. These mirror the document service's
// legacy canEditTaskFields/canChangeAssignment (tasks.go:486-498), split
// into named predicates per the brief's interface sketch so callers in
// tasks.go (and later Task 5's assignment/link handlers) can express
// intent directly instead of re-deriving the rule at each call site.
package service

import (
	"github.com/google/uuid"

	"github.com/aieera/sedoc/services/task/internal/model"
)

// isAdmin reports whether role grants tenant-admin authority over any
// task, regardless of who created or is assigned to it. "owner" is
// treated as a superset of "admin" (matches every other service's role
// hierarchy in this codebase).
func isAdmin(role string) bool {
	return role == "admin" || role == "owner"
}

// isAssignee reports whether userID is one of t's current assignees.
func isAssignee(t *model.Task, userID uuid.UUID) bool {
	for _, a := range t.Assignees {
		if a.UserID == userID {
			return true
		}
	}
	return false
}

// canEditFields gates the "core field" edit surface (title, description,
// priority, due date) and delete: creator or admin only. Being an
// assignee is deliberately NOT sufficient here — an assignee can act on
// a task (transition it, manage its links) without being able to rewrite
// what it says.
func canEditFields(t *model.Task, userID uuid.UUID, role string) bool {
	return t.CreatedBy == userID || isAdmin(role)
}

// canTransition gates status changes (complete/reopen/cancel): creator,
// any current assignee, or admin.
func canTransition(t *model.Task, userID uuid.UUID, role string) bool {
	if t.CreatedBy == userID {
		return true
	}
	if isAssignee(t, userID) {
		return true
	}
	return isAdmin(role)
}

// canManageLinks gates assignee/document link management. Same gate as
// canTransition per the brief — kept as a distinct named function (rather
// than an alias call site) so the two concerns can diverge later without
// a call-site rewrite.
func canManageLinks(t *model.Task, userID uuid.UUID, role string) bool {
	return canTransition(t, userID, role)
}
