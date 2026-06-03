// ADR 0068 — pure-function invariants for tasks.
//
// What we pin without a database:
//   - Priority validator accepts the four legal values + rejects others.
//   - Edit-fields permission is creator-or-admin only (assignee can't
//     rename someone else's task).
//   - Assignment / status changes accept creator OR assignee OR admin.
//   - Author bypass on the assignee path: a user assigned to a task
//     they didn't create can still complete or reassign.
//
// Notification + sweep flows need Postgres + an outbox publisher and
// run in the integration suite under -tags=integration.
package service

import (
	"testing"

	"github.com/google/uuid"

	"github.com/aieera/sedoc/services/document/internal/repository"
)

func TestValidTaskPriority(t *testing.T) {
	for _, p := range []string{"low", "normal", "high", "urgent"} {
		if !validTaskPriority(p) {
			t.Errorf("priority %q must be valid", p)
		}
	}
	for _, p := range []string{"", "URGENT", "blocker", "p0"} {
		if validTaskPriority(p) {
			t.Errorf("priority %q must be rejected (case-sensitive enum)", p)
		}
	}
}

func TestCanEditTaskFields_CreatorOrAdmin(t *testing.T) {
	creator := uuid.New()
	other := uuid.New()
	task := &repository.Task{CreatedBy: creator}

	if !canEditTaskFields(task, creator, "member") {
		t.Errorf("creator must always be allowed to edit")
	}
	if canEditTaskFields(task, other, "member") {
		t.Errorf("non-creator member must NOT edit")
	}
	if !canEditTaskFields(task, other, "admin") {
		t.Errorf("admin must edit")
	}
	if !canEditTaskFields(task, other, "owner") {
		t.Errorf("owner must edit")
	}
}

func TestCanChangeAssignment_CreatorAssigneeAdmin(t *testing.T) {
	creator := uuid.New()
	assignee := uuid.New()
	stranger := uuid.New()
	task := &repository.Task{CreatedBy: creator, AssigneeID: &assignee}

	if !canChangeAssignment(task, creator, "member") {
		t.Errorf("creator must change")
	}
	if !canChangeAssignment(task, assignee, "member") {
		t.Errorf("current assignee must change")
	}
	if canChangeAssignment(task, stranger, "member") {
		t.Errorf("unrelated member must NOT change")
	}
	if !canChangeAssignment(task, stranger, "admin") {
		t.Errorf("admin must change")
	}
}

func TestCanChangeAssignment_NoAssigneeYet(t *testing.T) {
	creator := uuid.New()
	task := &repository.Task{CreatedBy: creator}

	if canChangeAssignment(task, uuid.New(), "member") {
		t.Errorf("non-creator with no assignee yet must NOT be allowed")
	}
	if !canChangeAssignment(task, creator, "member") {
		t.Errorf("creator must always be allowed")
	}
}

// Pin the `assignee can complete but not rename` semantic — a
// classic user-experience expectation that's easy to break
// accidentally.
func TestPermissionAsymmetry_AssigneeCanCompleteButNotRename(t *testing.T) {
	creator := uuid.New()
	assignee := uuid.New()
	task := &repository.Task{CreatedBy: creator, AssigneeID: &assignee}

	if canEditTaskFields(task, assignee, "member") {
		t.Errorf("assignee must NOT rename someone else's task")
	}
	if !canChangeAssignment(task, assignee, "member") {
		t.Errorf("assignee must be allowed to complete the task")
	}
}
