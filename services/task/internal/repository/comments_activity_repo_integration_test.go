//go:build integration
// +build integration

// Task 3 (2026-07-28 task-service design) — smoke coverage for
// CommentRepository and ActivityRepository. Not called out by the
// brief's Step-1 test-list bullets (those are all TaskRepository
// scenarios), but later tasks depend on these interfaces' exact
// behavior, and task_comments.mentions is a Postgres UUID[] column —
// worth proving pgx v5 round-trips []uuid.UUID through it correctly
// rather than assuming it from the tasks-side tests alone.
package repository

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/services/task/internal/model"
)

func TestTasksRepo_CommentRepository_CRUDRoundTripsMentions(t *testing.T) {
	ctx, appPool, superPool := setupTaskDB(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	mentioned := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator)
	seedUser(ctx, t, superPool, tenant, mentioned, "Mentioned User")

	taskRepo := &taskRepo{}
	commentRepo := &commentRepo{}
	taskID := uuid.Must(uuid.NewV7())
	now := time.Now().UTC().Truncate(time.Microsecond)

	require.NoError(t, database.WithTenantTx(ctx, appPool, tenant, func(tx pgx.Tx) error {
		return taskRepo.Create(ctx, tx, &model.Task{
			TenantID: tenant, ID: taskID, Title: "Task with comments",
			Status: "open", Priority: "normal", Source: "user",
			CreatedBy: creator, CreatedAt: now, UpdatedAt: now,
		})
	}))

	commentID := uuid.Must(uuid.NewV7())
	comment := &model.TaskComment{
		TenantID: tenant, ID: commentID, TaskID: taskID, AuthorID: creator,
		Body: "hey @mentioned please look", Mentions: []uuid.UUID{mentioned},
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, database.WithTenantTx(ctx, appPool, tenant, func(tx pgx.Tx) error {
		return commentRepo.Create(ctx, tx, comment)
	}))

	var got *model.TaskComment
	require.NoError(t, database.WithTenantTx(ctx, appPool, tenant, func(tx pgx.Tx) error {
		var err error
		got, err = commentRepo.GetByID(ctx, tx, tenant, commentID)
		return err
	}))
	require.Equal(t, "hey @mentioned please look", got.Body)
	require.Equal(t, []uuid.UUID{mentioned}, got.Mentions, "UUID[] mentions column must round-trip through pgx as []uuid.UUID")

	var listed []model.TaskComment
	require.NoError(t, database.WithTenantTx(ctx, appPool, tenant, func(tx pgx.Tx) error {
		var err error
		listed, err = commentRepo.List(ctx, tx, tenant, taskID, 50, 0)
		return err
	}))
	require.Len(t, listed, 1)
	require.Equal(t, commentID, listed[0].ID)

	require.NoError(t, database.WithTenantTx(ctx, appPool, tenant, func(tx pgx.Tx) error {
		comment.Body = "edited body"
		comment.Mentions = nil
		return commentRepo.Update(ctx, tx, comment)
	}))
	require.NoError(t, database.WithTenantTx(ctx, appPool, tenant, func(tx pgx.Tx) error {
		var err error
		got, err = commentRepo.GetByID(ctx, tx, tenant, commentID)
		return err
	}))
	require.Equal(t, "edited body", got.Body)
	require.Empty(t, got.Mentions, "clearing mentions must round-trip to an empty (not nil-scan-error) array")

	require.NoError(t, database.WithTenantTx(ctx, appPool, tenant, func(tx pgx.Tx) error {
		return commentRepo.SoftDelete(ctx, tx, tenant, commentID)
	}))
	err := database.WithTenantTx(ctx, appPool, tenant, func(tx pgx.Tx) error {
		_, err := commentRepo.GetByID(ctx, tx, tenant, commentID)
		return err
	})
	require.ErrorIs(t, err, ErrNotFound)
}

func TestTasksRepo_ActivityRepository_InsertAndListNewestFirst(t *testing.T) {
	ctx, appPool, superPool := setupTaskDB(t)

	tenant := uuid.Must(uuid.NewV7())
	creator := uuid.Must(uuid.NewV7())
	seedOrgAndUser(ctx, t, superPool, tenant, creator)

	taskRepo := &taskRepo{}
	activityRepo := &activityRepo{}
	taskID := uuid.Must(uuid.NewV7())
	now := time.Now().UTC().Truncate(time.Microsecond)

	require.NoError(t, database.WithTenantTx(ctx, appPool, tenant, func(tx pgx.Tx) error {
		return taskRepo.Create(ctx, tx, &model.Task{
			TenantID: tenant, ID: taskID, Title: "Task with activity",
			Status: "open", Priority: "normal", Source: "user",
			CreatedBy: creator, CreatedAt: now, UpdatedAt: now,
		})
	}))

	require.NoError(t, database.WithTenantTx(ctx, appPool, tenant, func(tx pgx.Tx) error {
		a := &model.TaskActivity{TaskID: taskID, ActorID: creator, Action: "created"}
		if err := activityRepo.Insert(ctx, tx, tenant, a); err != nil {
			return err
		}
		require.NotZero(t, a.ID, "Insert must fill in the generated id")
		require.False(t, a.CreatedAt.IsZero(), "Insert must fill in created_at")

		detail, _ := json.Marshal(map[string]string{"from": "open", "to": "done"})
		b := &model.TaskActivity{TaskID: taskID, ActorID: creator, Action: "status_changed", Detail: detail}
		return activityRepo.Insert(ctx, tx, tenant, b)
	}))

	var listed []model.TaskActivity
	require.NoError(t, database.WithTenantTx(ctx, appPool, tenant, func(tx pgx.Tx) error {
		var err error
		listed, err = activityRepo.List(ctx, tx, tenant, taskID, 50, 0)
		return err
	}))
	require.Len(t, listed, 2)
	require.Equal(t, "status_changed", listed[0].Action, "List must be newest-first")
	require.JSONEq(t, `{"from":"open","to":"done"}`, string(listed[0].Detail))
	require.Equal(t, "created", listed[1].Action)
	require.JSONEq(t, `{}`, string(listed[1].Detail), "Insert must default a nil Detail to '{}' (NOT NULL column)")
}
