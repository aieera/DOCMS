// Task-3 (2026-07-28 task-service design) — `task_comments` repo.
package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/services/task/internal/model"
)

type commentRepo struct{}

// NewCommentRepo returns the default Postgres-backed CommentRepository impl.
func NewCommentRepo() CommentRepository { return &commentRepo{} }

const commentColumns = `tenant_id, id, task_id, author_id, body, mentions, created_at, updated_at, deleted_at`

func (r *commentRepo) Create(ctx context.Context, tx pgx.Tx, c *model.TaskComment) error {
	mentions := c.Mentions
	if mentions == nil {
		mentions = []uuid.UUID{}
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO task_comments (tenant_id, id, task_id, author_id, body, mentions, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		c.TenantID, c.ID, c.TaskID, c.AuthorID, c.Body, mentions, c.CreatedAt, c.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert task_comments: %w", err)
	}
	return nil
}

func (r *commentRepo) List(ctx context.Context, tx pgx.Tx, tenantID, taskID uuid.UUID, limit, offset int) ([]model.TaskComment, error) {
	rows, err := tx.Query(ctx, `
		SELECT `+commentColumns+`
		  FROM task_comments
		 WHERE tenant_id = $1 AND task_id = $2 AND deleted_at IS NULL
		 ORDER BY created_at ASC
		 LIMIT $3 OFFSET $4`, tenantID, taskID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list task_comments: %w", err)
	}
	defer rows.Close()

	var out []model.TaskComment
	for rows.Next() {
		c, err := scanComment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (r *commentRepo) GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.TaskComment, error) {
	row := tx.QueryRow(ctx, `
		SELECT `+commentColumns+`
		  FROM task_comments WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`, tenantID, id)
	c, err := scanComment(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return c, nil
}

func (r *commentRepo) Update(ctx context.Context, tx pgx.Tx, c *model.TaskComment) error {
	mentions := c.Mentions
	if mentions == nil {
		mentions = []uuid.UUID{}
	}
	tag, err := tx.Exec(ctx, `
		UPDATE task_comments SET body = $3, mentions = $4, updated_at = now()
		 WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`,
		c.TenantID, c.ID, c.Body, mentions)
	if err != nil {
		return fmt.Errorf("update task_comments: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *commentRepo) SoftDelete(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error {
	tag, err := tx.Exec(ctx, `
		UPDATE task_comments SET deleted_at = now(), updated_at = now()
		 WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`, tenantID, id)
	if err != nil {
		return fmt.Errorf("soft delete task_comments: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func scanComment(row rowScanner) (*model.TaskComment, error) {
	var c model.TaskComment
	if err := row.Scan(
		&c.TenantID, &c.ID, &c.TaskID, &c.AuthorID, &c.Body, &c.Mentions,
		&c.CreatedAt, &c.UpdatedAt, &c.DeletedAt,
	); err != nil {
		return nil, err
	}
	return &c, nil
}
