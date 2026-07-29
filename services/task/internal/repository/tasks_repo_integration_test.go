//go:build integration
// +build integration

// Task 3 (2026-07-28 task-service design) — repository layer tests for
// the `tasks` aggregate (tasks + task_assignees + task_documents +
// task_comments + task_activity).
//
// Run with:
//
//	go test -tags integration -race -run TestTasksRepo ./services/task/internal/repository/
package repository

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/services/task/internal/model"
)

// TestTasksRepo_Create_AggregatesAssigneesAndDocuments covers the brief's
// first bullet: create a task with 2 assignees + 1 document, then
// GetByID must return both aggregated.
func TestTasksRepo_Create_AggregatesAssigneesAndDocuments(t *testing.T) {
	ctx, appPool, superPool := setupTaskDB(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	assignee1 := uuid.Must(uuid.NewV7())
	assignee2 := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator)
	seedUser(ctx, t, superPool, tenant, assignee1, "Assignee One")
	seedUser(ctx, t, superPool, tenant, assignee2, "Assignee Two")
	docID, wsID := seedDocument(ctx, t, superPool, tenant, "Contract v1")

	repo := &taskRepo{}
	taskID := uuid.Must(uuid.NewV7())
	now := time.Now().UTC().Truncate(time.Microsecond)

	task := &model.Task{
		TenantID: tenant, ID: taskID, Title: "Review contract",
		Status: "open", Priority: "normal", Source: "user",
		CreatedBy: creator, CreatedAt: now, UpdatedAt: now,
		Assignees: []model.TaskAssignee{
			{UserID: assignee1, AddedBy: creator, AddedAt: now},
			{UserID: assignee2, AddedBy: creator, AddedAt: now},
		},
		Documents: []model.TaskDocument{
			{DocumentID: docID, WorkspaceID: wsID, TitleSnapshot: "Contract v1", LinkedBy: creator, LinkedAt: now},
		},
	}

	require.NoError(t, database.WithTenantTx(ctx, appPool, tenant, func(tx pgx.Tx) error {
		return repo.Create(ctx, tx, task)
	}))

	var got *model.Task
	require.NoError(t, database.WithTenantTx(ctx, appPool, tenant, func(tx pgx.Tx) error {
		var err error
		got, err = repo.GetByID(ctx, tx, tenant, taskID)
		return err
	}))

	require.Equal(t, "Review contract", got.Title)
	require.Len(t, got.Assignees, 2, "both assignees must be aggregated")
	gotAssignees := []uuid.UUID{got.Assignees[0].UserID, got.Assignees[1].UserID}
	require.ElementsMatch(t, []uuid.UUID{assignee1, assignee2}, gotAssignees)

	require.Len(t, got.Documents, 1, "the linked document must be aggregated")
	require.Equal(t, docID, got.Documents[0].DocumentID)
	require.Equal(t, wsID, got.Documents[0].WorkspaceID)
	require.Equal(t, "Contract v1", got.Documents[0].TitleSnapshot)
}

// TestTasksRepo_List_FiltersByAssignee covers the brief's second bullet:
// List with AssigneeID returns the task for each of its own assignees,
// and not for a third user who isn't assigned to anything.
func TestTasksRepo_List_FiltersByAssignee(t *testing.T) {
	ctx, appPool, superPool := setupTaskDB(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	userA := uuid.Must(uuid.NewV7())
	userB := uuid.Must(uuid.NewV7())
	userC := uuid.Must(uuid.NewV7()) // assigned to nothing
	seedOrgAndUser(ctx, t, superPool, tenant, creator)
	seedUser(ctx, t, superPool, tenant, userA, "User A")
	seedUser(ctx, t, superPool, tenant, userB, "User B")
	seedUser(ctx, t, superPool, tenant, userC, "User C")

	repo := &taskRepo{}
	taskAID := uuid.Must(uuid.NewV7())
	taskBID := uuid.Must(uuid.NewV7())
	now := time.Now().UTC().Truncate(time.Microsecond)

	require.NoError(t, database.WithTenantTx(ctx, appPool, tenant, func(tx pgx.Tx) error {
		if err := repo.Create(ctx, tx, &model.Task{
			TenantID: tenant, ID: taskAID, Title: "Task for A",
			Status: "open", Priority: "normal", Source: "user",
			CreatedBy: creator, CreatedAt: now, UpdatedAt: now,
			Assignees: []model.TaskAssignee{{UserID: userA, AddedBy: creator, AddedAt: now}},
		}); err != nil {
			return err
		}
		return repo.Create(ctx, tx, &model.Task{
			TenantID: tenant, ID: taskBID, Title: "Task for B",
			Status: "open", Priority: "normal", Source: "user",
			CreatedBy: creator, CreatedAt: now, UpdatedAt: now,
			Assignees: []model.TaskAssignee{{UserID: userB, AddedBy: creator, AddedAt: now}},
		})
	}))

	cases := []struct {
		name   string
		user   uuid.UUID
		wantID *uuid.UUID
	}{
		{"assignee A sees task A", userA, &taskAID},
		{"assignee B sees task B", userB, &taskBID},
		{"non-assignee C sees nothing", userC, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var tasks []model.Task
			var total int
			require.NoError(t, database.WithTenantTx(ctx, appPool, tenant, func(tx pgx.Tx) error {
				var err error
				tasks, total, err = repo.List(ctx, tx, tenant, TaskFilters{AssigneeID: &tc.user})
				return err
			}))
			if tc.wantID == nil {
				require.Zero(t, total)
				require.Empty(t, tasks)
				return
			}
			require.Equal(t, 1, total)
			require.Len(t, tasks, 1)
			require.Equal(t, *tc.wantID, tasks[0].ID)
		})
	}
}

// TestTasksRepo_List_PaginationHonorsLimitAndTotal covers the brief's
// third bullet: List pagination returns total=N with the page size
// honored.
func TestTasksRepo_List_PaginationHonorsLimitAndTotal(t *testing.T) {
	ctx, appPool, superPool := setupTaskDB(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator)

	repo := &taskRepo{}
	const n = 5
	now := time.Now().UTC().Truncate(time.Microsecond)
	allIDs := make(map[uuid.UUID]bool, n)

	require.NoError(t, database.WithTenantTx(ctx, appPool, tenant, func(tx pgx.Tx) error {
		for i := 0; i < n; i++ {
			id := uuid.Must(uuid.NewV7())
			allIDs[id] = true
			if err := repo.Create(ctx, tx, &model.Task{
				TenantID: tenant, ID: id, Title: fmt.Sprintf("Task %d", i),
				Status: "open", Priority: "normal", Source: "user",
				CreatedBy: creator, CreatedAt: now.Add(time.Duration(i) * time.Second), UpdatedAt: now,
			}); err != nil {
				return err
			}
		}
		return nil
	}))

	var page1, page2, page3 []model.Task
	var total1, total2, total3 int
	require.NoError(t, database.WithTenantTx(ctx, appPool, tenant, func(tx pgx.Tx) error {
		var err error
		page1, total1, err = repo.List(ctx, tx, tenant, TaskFilters{Limit: 2, Offset: 0})
		return err
	}))
	require.NoError(t, database.WithTenantTx(ctx, appPool, tenant, func(tx pgx.Tx) error {
		var err error
		page2, total2, err = repo.List(ctx, tx, tenant, TaskFilters{Limit: 2, Offset: 2})
		return err
	}))
	require.NoError(t, database.WithTenantTx(ctx, appPool, tenant, func(tx pgx.Tx) error {
		var err error
		page3, total3, err = repo.List(ctx, tx, tenant, TaskFilters{Limit: 2, Offset: 4})
		return err
	}))

	require.Equal(t, n, total1)
	require.Equal(t, n, total2)
	require.Equal(t, n, total3)
	require.Len(t, page1, 2)
	require.Len(t, page2, 2)
	require.Len(t, page3, 1, "last page holds the remainder (5 total, page size 2)")

	seen := map[uuid.UUID]bool{}
	for _, p := range [][]model.Task{page1, page2, page3} {
		for _, task := range p {
			require.True(t, allIDs[task.ID], "returned task id must be one we created")
			require.False(t, seen[task.ID], "no task should appear on two pages")
			seen[task.ID] = true
		}
	}
	require.Len(t, seen, n, "all N tasks must be reachable across pages")
}

// TestTasksRepo_List_QueryMatchesTitleCaseInsensitive covers the brief's
// fourth bullet: ILIKE query matches title.
func TestTasksRepo_List_QueryMatchesTitleCaseInsensitive(t *testing.T) {
	ctx, appPool, superPool := setupTaskDB(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator)

	repo := &taskRepo{}
	matchID := uuid.Must(uuid.NewV7())
	otherID := uuid.Must(uuid.NewV7())
	now := time.Now().UTC().Truncate(time.Microsecond)

	require.NoError(t, database.WithTenantTx(ctx, appPool, tenant, func(tx pgx.Tx) error {
		if err := repo.Create(ctx, tx, &model.Task{
			TenantID: tenant, ID: matchID, Title: "Renew vendor Contract",
			Status: "open", Priority: "normal", Source: "user",
			CreatedBy: creator, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return err
		}
		return repo.Create(ctx, tx, &model.Task{
			TenantID: tenant, ID: otherID, Title: "Book flight",
			Status: "open", Priority: "normal", Source: "user",
			CreatedBy: creator, CreatedAt: now, UpdatedAt: now,
		})
	}))

	var tasks []model.Task
	var total int
	require.NoError(t, database.WithTenantTx(ctx, appPool, tenant, func(tx pgx.Tx) error {
		var err error
		tasks, total, err = repo.List(ctx, tx, tenant, TaskFilters{Query: "CONTRACT"})
		return err
	}))

	require.Equal(t, 1, total)
	require.Len(t, tasks, 1)
	require.Equal(t, matchID, tasks[0].ID)
}

// TestTasksRepo_SoftDelete_HidesFromGetByIDAndList covers the brief's
// fifth bullet: a soft-deleted task disappears from Get/List.
func TestTasksRepo_SoftDelete_HidesFromGetByIDAndList(t *testing.T) {
	ctx, appPool, superPool := setupTaskDB(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator)

	repo := &taskRepo{}
	taskID := uuid.Must(uuid.NewV7())
	now := time.Now().UTC().Truncate(time.Microsecond)

	require.NoError(t, database.WithTenantTx(ctx, appPool, tenant, func(tx pgx.Tx) error {
		return repo.Create(ctx, tx, &model.Task{
			TenantID: tenant, ID: taskID, Title: "To be deleted",
			Status: "open", Priority: "normal", Source: "user",
			CreatedBy: creator, CreatedAt: now, UpdatedAt: now,
		})
	}))

	require.NoError(t, database.WithTenantTx(ctx, appPool, tenant, func(tx pgx.Tx) error {
		return repo.SoftDelete(ctx, tx, tenant, taskID)
	}))

	err := database.WithTenantTx(ctx, appPool, tenant, func(tx pgx.Tx) error {
		_, err := repo.GetByID(ctx, tx, tenant, taskID)
		return err
	})
	require.ErrorIs(t, err, ErrNotFound)

	var total int
	require.NoError(t, database.WithTenantTx(ctx, appPool, tenant, func(tx pgx.Tx) error {
		var err error
		_, total, err = repo.List(ctx, tx, tenant, TaskFilters{IncludeCompleted: true})
		return err
	}))
	require.Zero(t, total, "soft-deleted task must not appear in List even with IncludeCompleted")

	// A second SoftDelete on the already-deleted row must report
	// ErrNotFound too (RowsAffected==0), not silently succeed.
	err = database.WithTenantTx(ctx, appPool, tenant, func(tx pgx.Tx) error {
		return repo.SoftDelete(ctx, tx, tenant, taskID)
	})
	require.ErrorIs(t, err, ErrNotFound)
}

// TestTasksRepo_AddAssignee_IsIdempotent covers the brief's sixth bullet.
func TestTasksRepo_AddAssignee_IsIdempotent(t *testing.T) {
	ctx, appPool, superPool := setupTaskDB(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	assignee := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator)
	seedUser(ctx, t, superPool, tenant, assignee, "Assignee")

	repo := &taskRepo{}
	taskID := uuid.Must(uuid.NewV7())
	now := time.Now().UTC().Truncate(time.Microsecond)

	require.NoError(t, database.WithTenantTx(ctx, appPool, tenant, func(tx pgx.Tx) error {
		return repo.Create(ctx, tx, &model.Task{
			TenantID: tenant, ID: taskID, Title: "Needs an assignee",
			Status: "open", Priority: "normal", Source: "user",
			CreatedBy: creator, CreatedAt: now, UpdatedAt: now,
		})
	}))

	for i := 0; i < 2; i++ {
		require.NoError(t, database.WithTenantTx(ctx, appPool, tenant, func(tx pgx.Tx) error {
			return repo.AddAssignee(ctx, tx, tenant, taskID, assignee, creator)
		}), "AddAssignee call %d must not error", i+1)
	}

	var got *model.Task
	require.NoError(t, database.WithTenantTx(ctx, appPool, tenant, func(tx pgx.Tx) error {
		var err error
		got, err = repo.GetByID(ctx, tx, tenant, taskID)
		return err
	}))
	require.Len(t, got.Assignees, 1, "repeated AddAssignee must not duplicate the row")
	require.Equal(t, assignee, got.Assignees[0].UserID)
}

// TestTasksRepo_LinkDocument_SnapshotsTitleAndErrorsOnMissingDocument
// covers the brief's seventh bullet.
func TestTasksRepo_LinkDocument_SnapshotsTitleAndErrorsOnMissingDocument(t *testing.T) {
	ctx, appPool, superPool := setupTaskDB(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator)
	docID, wsID := seedDocument(ctx, t, superPool, tenant, "Spec v2")

	repo := &taskRepo{}
	taskID := uuid.Must(uuid.NewV7())
	now := time.Now().UTC().Truncate(time.Microsecond)

	require.NoError(t, database.WithTenantTx(ctx, appPool, tenant, func(tx pgx.Tx) error {
		return repo.Create(ctx, tx, &model.Task{
			TenantID: tenant, ID: taskID, Title: "Needs a document",
			Status: "open", Priority: "normal", Source: "user",
			CreatedBy: creator, CreatedAt: now, UpdatedAt: now,
		})
	}))

	var linked *model.TaskDocument
	require.NoError(t, database.WithTenantTx(ctx, appPool, tenant, func(tx pgx.Tx) error {
		var err error
		linked, err = repo.LinkDocument(ctx, tx, tenant, taskID, docID, creator)
		return err
	}))
	require.Equal(t, docID, linked.DocumentID)
	require.Equal(t, wsID, linked.WorkspaceID)
	require.Equal(t, "Spec v2", linked.TitleSnapshot, "title must be snapshotted from documents.title at link time")

	bogusDocID := uuid.Must(uuid.NewV7())
	err := database.WithTenantTx(ctx, appPool, tenant, func(tx pgx.Tx) error {
		_, err := repo.LinkDocument(ctx, tx, tenant, taskID, bogusDocID, creator)
		return err
	})
	require.ErrorIs(t, err, ErrNotFound, "linking a non-existent document must fail with ErrNotFound")
}

// TestTasksRepo_ClaimDueSoonAndOverdue exercises the ported sweep
// queries: ClaimDueSoon must claim (and stamp reminded_at on) exactly the
// task due within 24h that hasn't been reminded, and ClaimOverdue must
// claim exactly the task whose due_at has already passed and hasn't been
// overdue-pinged — each exactly once (the second call finds nothing left
// to claim).
func TestTasksRepo_ClaimDueSoonAndOverdue(t *testing.T) {
	ctx, appPool, superPool := setupTaskDB(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator)

	repo := &taskRepo{}
	now := time.Now().UTC().Truncate(time.Microsecond)
	dueSoonAt := now.Add(2 * time.Hour)
	overdueAt := now.Add(-2 * time.Hour)
	futureAt := now.Add(30 * 24 * time.Hour)

	dueSoonID := uuid.Must(uuid.NewV7())
	overdueID := uuid.Must(uuid.NewV7())
	futureID := uuid.Must(uuid.NewV7())

	require.NoError(t, database.WithTenantTx(ctx, appPool, tenant, func(tx pgx.Tx) error {
		for id, due := range map[uuid.UUID]time.Time{dueSoonID: dueSoonAt, overdueID: overdueAt, futureID: futureAt} {
			if err := repo.Create(ctx, tx, &model.Task{
				TenantID: tenant, ID: id, Title: "sweep candidate",
				Status: "open", Priority: "normal", Source: "user",
				CreatedBy: creator, CreatedAt: now, UpdatedAt: now,
				DueAt: timePtr(due),
			}); err != nil {
				return err
			}
		}
		return nil
	}))

	var dueSoonClaimed []model.Task
	require.NoError(t, database.WithTenantTx(ctx, appPool, tenant, func(tx pgx.Tx) error {
		var err error
		dueSoonClaimed, err = repo.ClaimDueSoon(ctx, tx, now)
		return err
	}))
	require.Len(t, dueSoonClaimed, 1)
	require.Equal(t, dueSoonID, dueSoonClaimed[0].ID)
	require.NotNil(t, dueSoonClaimed[0].RemindedAt)

	var overdueClaimed []model.Task
	require.NoError(t, database.WithTenantTx(ctx, appPool, tenant, func(tx pgx.Tx) error {
		var err error
		overdueClaimed, err = repo.ClaimOverdue(ctx, tx, now)
		return err
	}))
	require.Len(t, overdueClaimed, 1)
	require.Equal(t, overdueID, overdueClaimed[0].ID)
	require.NotNil(t, overdueClaimed[0].OverdueNotifiedAt)

	// Re-running either claim must find nothing left (single-shot flags).
	var dueSoonAgain, overdueAgain []model.Task
	require.NoError(t, database.WithTenantTx(ctx, appPool, tenant, func(tx pgx.Tx) error {
		var err error
		dueSoonAgain, err = repo.ClaimDueSoon(ctx, tx, now)
		return err
	}))
	require.NoError(t, database.WithTenantTx(ctx, appPool, tenant, func(tx pgx.Tx) error {
		var err error
		overdueAgain, err = repo.ClaimOverdue(ctx, tx, now)
		return err
	}))
	require.Empty(t, dueSoonAgain)
	require.Empty(t, overdueAgain)
}

func timePtr(t time.Time) *time.Time { return &t }
