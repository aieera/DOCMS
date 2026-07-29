// Task 4 (2026-07-28 task-service design) — table-driven unit coverage
// for permissions.go's pure predicates. No DB, no ctx — these run in
// milliseconds and pin down the creator/assignee/admin/stranger ×
// edit/transition matrix the brief calls out explicitly.
package service

import (
	"testing"

	"github.com/google/uuid"

	"github.com/aieera/sedoc/services/task/internal/model"
)

func TestIsAdmin(t *testing.T) {
	cases := []struct {
		role string
		want bool
	}{
		{"admin", true},
		{"owner", true},
		{"member", false},
		{"", false},
		{"Admin", false}, // case-sensitive: roles come from the DB verbatim
	}
	for _, c := range cases {
		t.Run(c.role, func(t *testing.T) {
			if got := isAdmin(c.role); got != c.want {
				t.Errorf("isAdmin(%q) = %v, want %v", c.role, got, c.want)
			}
		})
	}
}

func TestIsAssignee(t *testing.T) {
	assignee := uuid.New()
	other := uuid.New()
	task := &model.Task{
		Assignees: []model.TaskAssignee{{UserID: assignee}},
	}

	if !isAssignee(task, assignee) {
		t.Error("isAssignee: expected true for a listed assignee")
	}
	if isAssignee(task, other) {
		t.Error("isAssignee: expected false for a user not in Assignees")
	}
	if isAssignee(&model.Task{}, assignee) {
		t.Error("isAssignee: expected false when Assignees is empty/nil")
	}
}

// TestPermissionMatrix is the brief's explicit ask: creator/assignee/
// admin/stranger × edit-fields/transition/manage-links, in one table so
// the four-actor semantics are visible at a glance.
func TestPermissionMatrix(t *testing.T) {
	creator := uuid.New()
	assignee := uuid.New()
	stranger := uuid.New()
	adminUser := uuid.New()

	task := &model.Task{
		CreatedBy: creator,
		Assignees: []model.TaskAssignee{{UserID: assignee}},
	}

	cases := []struct {
		name           string
		userID         uuid.UUID
		role           string
		wantEditFields bool
		wantTransition bool
		wantManageLink bool
	}{
		{"creator/member", creator, "member", true, true, true},
		{"assignee/member", assignee, "member", false, true, true},
		{"stranger/member", stranger, "member", false, false, false},
		{"stranger/admin", adminUser, "admin", true, true, true},
		{"stranger/owner", adminUser, "owner", true, true, true},
		{"creator-and-assignee/member", creator, "member", true, true, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := canEditFields(task, c.userID, c.role); got != c.wantEditFields {
				t.Errorf("canEditFields = %v, want %v", got, c.wantEditFields)
			}
			if got := canTransition(task, c.userID, c.role); got != c.wantTransition {
				t.Errorf("canTransition = %v, want %v", got, c.wantTransition)
			}
			if got := canManageLinks(task, c.userID, c.role); got != c.wantManageLink {
				t.Errorf("canManageLinks = %v, want %v", got, c.wantManageLink)
			}
		})
	}
}

// TestCanEditFields_AssigneeAloneIsNotEnough pins down the one asymmetry
// in the matrix: being assigned grants transition/link-management rights
// but NOT the right to rewrite title/description/priority/due date.
func TestCanEditFields_AssigneeAloneIsNotEnough(t *testing.T) {
	creator := uuid.New()
	assignee := uuid.New()
	task := &model.Task{
		CreatedBy: creator,
		Assignees: []model.TaskAssignee{{UserID: assignee}},
	}
	if canEditFields(task, assignee, "member") {
		t.Error("an assignee who is not the creator/admin must not be able to edit core fields")
	}
	if !canTransition(task, assignee, "member") {
		t.Error("an assignee must be able to transition the task")
	}
}
