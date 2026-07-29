//go:build integration
// +build integration

// Task 6 (2026-07-28 task-service design) — integration coverage for
// SweepTaskNotifications against the real Postgres schema, reusing
// taskServiceFixture/seedOrgAndUser/seedUser/callerCtx/outboxPayloads/
// outboxEventTypes/countStr/stringSlice from
// tasks_service_integration_test.go (same package, same file group).
//
// Run with:
//
//	go test -tags integration -race -run TestSweep ./services/task/internal/service/
package service_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/services/task/internal/service"
)

// TestSweepTaskNotifications_DueSoon_NotifiesAllAssignees_IdempotentOnRerun
// covers the brief's headline sweep scenario: a task due in 2h (inside
// the 24h due-soon window) with two assignees. One sweep run must emit
// exactly one dms.notify.task.due_soon.v1 whose user_ids lists both
// assignees; a second sweep run immediately after must emit nothing more
// — the repo's UPDATE...RETURNING claim already stamped reminded_at, so
// the task no longer matches ClaimDueSoon's WHERE reminded_at IS NULL.
func TestSweepTaskNotifications_DueSoon_NotifiesAllAssignees_IdempotentOnRerun(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	assigneeB := uuid.Must(uuid.NewV7())
	assigneeC := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator, "member")
	seedUser(ctx, t, superPool, tenant, assigneeB, "member")
	seedUser(ctx, t, superPool, tenant, assigneeC, "member")

	creatorCtx := callerCtx(ctx, tenant, creator, "member")
	due := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Microsecond)
	task, err := svc.CreateTask(creatorCtx, service.CreateTaskInput{
		Title: "Due soon", DueAt: &due, AssigneeIDs: []uuid.UUID{assigneeB, assigneeC},
	})
	require.NoError(t, err)

	require.NoError(t, svc.SweepTaskNotifications(ctx))

	events := outboxEventTypes(ctx, t, superPool, tenant)
	require.Equal(t, 1, countStr(events, "dms.notify.task.due_soon.v1"))

	notifies := outboxPayloads(ctx, t, superPool, tenant, "dms.notify.task.due_soon.v1")
	require.Len(t, notifies, 1)
	require.Equal(t, "task.due_soon", notifies[0]["type"])
	require.ElementsMatch(t, []string{assigneeB.String(), assigneeC.String()}, stringSlice(notifies[0]["user_ids"]),
		"due-soon notify must target every current assignee")

	var remindedAt *time.Time
	require.NoError(t, superPool.QueryRow(ctx, `SELECT reminded_at FROM tasks WHERE tenant_id = $1 AND id = $2`, tenant, task.ID).Scan(&remindedAt))
	require.NotNil(t, remindedAt, "the claim must stamp reminded_at")

	// Second run: claim idempotence — reminded_at is no longer NULL, so
	// ClaimDueSoon must not re-select this task.
	require.NoError(t, svc.SweepTaskNotifications(ctx))
	eventsAfter := outboxEventTypes(ctx, t, superPool, tenant)
	require.Equal(t, 1, countStr(eventsAfter, "dms.notify.task.due_soon.v1"), "a second sweep run must not re-notify an already-claimed task")
}

// TestSweepTaskNotifications_DueSoon_ZeroAssignees_EmitsNothing covers
// the brief's "tasks with zero assignees: due_soon emits nothing" rule.
// The claim itself still happens (reminded_at gets stamped, so a
// re-assign-then-resweep can't double-fire), but emitNotify no-ops on an
// empty recipient list.
func TestSweepTaskNotifications_DueSoon_ZeroAssignees_EmitsNothing(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator, "member")

	creatorCtx := callerCtx(ctx, tenant, creator, "member")
	due := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Microsecond)
	_, err := svc.CreateTask(creatorCtx, service.CreateTaskInput{Title: "Due soon, unassigned", DueAt: &due})
	require.NoError(t, err)

	require.NoError(t, svc.SweepTaskNotifications(ctx))

	events := outboxEventTypes(ctx, t, superPool, tenant)
	require.Zero(t, countStr(events, "dms.notify.task.due_soon.v1"), "a task with no assignees must not emit a due-soon notify")
}

// TestSweepTaskNotifications_Overdue_NotifiesAssigneesUnionCreator covers
// the overdue recipient rule: assignees ∪ creator, deduped. The creator
// is also an assignee here, so the notify's user_ids must contain
// exactly {creator, otherAssignee} — two entries, not three.
func TestSweepTaskNotifications_Overdue_NotifiesAssigneesUnionCreator(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	otherAssignee := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator, "member")
	seedUser(ctx, t, superPool, tenant, otherAssignee, "member")

	creatorCtx := callerCtx(ctx, tenant, creator, "member")
	overdueDue := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Microsecond)
	task, err := svc.CreateTask(creatorCtx, service.CreateTaskInput{
		Title: "Overdue", DueAt: &overdueDue, AssigneeIDs: []uuid.UUID{creator, otherAssignee},
	})
	require.NoError(t, err)

	require.NoError(t, svc.SweepTaskNotifications(ctx))

	events := outboxEventTypes(ctx, t, superPool, tenant)
	require.Equal(t, 1, countStr(events, "dms.notify.task.overdue.v1"))
	require.Zero(t, countStr(events, "dms.notify.task.due_soon.v1"), "an already-overdue task must not also fire a due-soon notify")

	notifies := outboxPayloads(ctx, t, superPool, tenant, "dms.notify.task.overdue.v1")
	require.Len(t, notifies, 1)
	require.Equal(t, "task.overdue", notifies[0]["type"])
	require.ElementsMatch(t, []string{creator.String(), otherAssignee.String()}, stringSlice(notifies[0]["user_ids"]),
		"overdue notify must be the deduped union of assignees and creator")

	var overdueNotifiedAt *time.Time
	require.NoError(t, superPool.QueryRow(ctx, `SELECT overdue_notified_at FROM tasks WHERE tenant_id = $1 AND id = $2`, tenant, task.ID).Scan(&overdueNotifiedAt))
	require.NotNil(t, overdueNotifiedAt)

	// Idempotence, mirroring the due-soon case.
	require.NoError(t, svc.SweepTaskNotifications(ctx))
	eventsAfter := outboxEventTypes(ctx, t, superPool, tenant)
	require.Equal(t, 1, countStr(eventsAfter, "dms.notify.task.overdue.v1"), "a second sweep run must not re-notify an already-claimed overdue task")
}

// TestSweepTaskNotifications_Overdue_ZeroAssignees_StillNotifiesCreator
// covers the brief's explicit exception: unlike due-soon, an overdue
// task with no assignees still pings its creator.
func TestSweepTaskNotifications_Overdue_ZeroAssignees_StillNotifiesCreator(t *testing.T) {
	ctx, svc, superPool := taskServiceFixture(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator, "member")

	creatorCtx := callerCtx(ctx, tenant, creator, "member")
	overdueDue := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Microsecond)
	_, err := svc.CreateTask(creatorCtx, service.CreateTaskInput{Title: "Overdue, unassigned", DueAt: &overdueDue})
	require.NoError(t, err)

	require.NoError(t, svc.SweepTaskNotifications(ctx))

	notifies := outboxPayloads(ctx, t, superPool, tenant, "dms.notify.task.overdue.v1")
	require.Len(t, notifies, 1)
	require.ElementsMatch(t, []string{creator.String()}, stringSlice(notifies[0]["user_ids"]),
		"an unassigned overdue task must still notify its creator")
}
