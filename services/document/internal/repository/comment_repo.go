// ADR 0066 — comments + comment_reactions repository.
//
// The `comments` table has shipped since the initial schema; this
// file is the first set of bindings against it. `comment_reactions`
// is brand-new in migration 000031.
//
// All methods require a tenant-scoped tx (RLS enforces isolation
// at the DB layer; we still pass tenant_id explicitly for the
// composite-PK clauses).
package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
)

// Comment is the row shape used by the service layer.
type Comment struct {
	TenantID        uuid.UUID
	ID              uuid.UUID
	DocumentID      uuid.UUID
	VersionID       *uuid.UUID
	ParentCommentID *uuid.UUID
	AuthorID        uuid.UUID
	Body            string
	IsResolved      bool
	ResolvedBy      *uuid.UUID
	ResolvedAt      *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
	DeletedAt       *time.Time
}

// CommentReaction is one (user, emoji) tag on a comment.
type CommentReaction struct {
	TenantID  uuid.UUID
	CommentID uuid.UUID
	UserID    uuid.UUID
	Emoji     string
	CreatedAt time.Time
}

// CommentRepository is the comment + reaction persistence interface.
// Annotation repo's `pgx.Tx`-wrapping pattern is mirrored here.
type CommentRepository interface {
	// ---- comments ----------------------------------------------------
	Create(ctx context.Context, tx pgx.Tx, c *Comment) error
	GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*Comment, error)
	ListByDocument(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID, includeResolved bool) ([]Comment, error)
	UpdateBody(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, body string) error
	SoftDelete(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error
	SetResolved(ctx context.Context, tx pgx.Tx, tenantID, id, resolvedBy uuid.UUID, resolved bool) error

	// ---- reactions ---------------------------------------------------
	AddReaction(ctx context.Context, tx pgx.Tx, r *CommentReaction) error
	RemoveReaction(ctx context.Context, tx pgx.Tx, tenantID, commentID, userID uuid.UUID, emoji string) error
	ListReactions(ctx context.Context, tx pgx.Tx, tenantID, commentID uuid.UUID) ([]CommentReaction, error)
}

// NewCommentRepo returns the default Postgres-backed impl.
func NewCommentRepo() CommentRepository { return &commentRepo{} }

type commentRepo struct{}

// ---- comments ----------------------------------------------------------

func (r *commentRepo) Create(ctx context.Context, tx pgx.Tx, c *Comment) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO comments (
			tenant_id, id, document_id, version_id, parent_comment_id,
			author_id, body, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		c.TenantID, c.ID, c.DocumentID, c.VersionID, c.ParentCommentID,
		c.AuthorID, c.Body, c.CreatedAt, c.UpdatedAt)
	return err
}

func (r *commentRepo) GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*Comment, error) {
	var c Comment
	err := tx.QueryRow(ctx, `
		SELECT tenant_id, id, document_id, version_id, parent_comment_id,
		       author_id, body, is_resolved, resolved_by, resolved_at,
		       created_at, updated_at, deleted_at
		  FROM comments
		 WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`,
		tenantID, id,
	).Scan(&c.TenantID, &c.ID, &c.DocumentID, &c.VersionID, &c.ParentCommentID,
		&c.AuthorID, &c.Body, &c.IsResolved, &c.ResolvedBy, &c.ResolvedAt,
		&c.CreatedAt, &c.UpdatedAt, &c.DeletedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, vdmserr.ErrNotFound
		}
		return nil, err
	}
	return &c, nil
}

// ListByDocument returns every non-deleted comment on the document
// ordered by created_at ASC. Threading happens client-side via
// parent_comment_id; sending the flat list keeps the query simple
// and lets the frontend group by thread root cheaply.
//
// includeResolved=false hides resolved THREADS (root + replies).
// Replies aren't independently filterable; the resolve flag lives
// on the root.
func (r *commentRepo) ListByDocument(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID, includeResolved bool) ([]Comment, error) {
	q := `
		SELECT tenant_id, id, document_id, version_id, parent_comment_id,
		       author_id, body, is_resolved, resolved_by, resolved_at,
		       created_at, updated_at, deleted_at
		  FROM comments
		 WHERE tenant_id = $1 AND document_id = $2 AND deleted_at IS NULL`
	if !includeResolved {
		q += `
		   AND id NOT IN (
		     SELECT id FROM comments WHERE tenant_id = $1 AND is_resolved = TRUE
		   )
		   AND COALESCE(parent_comment_id, id) NOT IN (
		     SELECT id FROM comments WHERE tenant_id = $1 AND is_resolved = TRUE
		   )`
	}
	q += ` ORDER BY created_at ASC`

	rows, err := tx.Query(ctx, q, tenantID, documentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Comment
	for rows.Next() {
		var c Comment
		if err := rows.Scan(&c.TenantID, &c.ID, &c.DocumentID, &c.VersionID, &c.ParentCommentID,
			&c.AuthorID, &c.Body, &c.IsResolved, &c.ResolvedBy, &c.ResolvedAt,
			&c.CreatedAt, &c.UpdatedAt, &c.DeletedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *commentRepo) UpdateBody(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, body string) error {
	tag, err := tx.Exec(ctx,
		`UPDATE comments SET body = $3, updated_at = now()
		   WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`,
		tenantID, id, body)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

func (r *commentRepo) SoftDelete(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error {
	tag, err := tx.Exec(ctx,
		`UPDATE comments SET deleted_at = now(), updated_at = now()
		   WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`,
		tenantID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

// SetResolved flips the resolve flag on a comment. Per ADR 0066 the
// service layer rejects resolving a reply (parent_comment_id IS NOT
// NULL) so by the time we land here `id` is always a thread root.
func (r *commentRepo) SetResolved(ctx context.Context, tx pgx.Tx, tenantID, id, resolvedBy uuid.UUID, resolved bool) error {
	var (
		rb        *uuid.UUID
		ra        *time.Time
	)
	if resolved {
		now := time.Now().UTC()
		rb, ra = &resolvedBy, &now
	}
	tag, err := tx.Exec(ctx, `
		UPDATE comments
		   SET is_resolved = $3, resolved_by = $4, resolved_at = $5, updated_at = now()
		 WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`,
		tenantID, id, resolved, rb, ra)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

// ---- reactions ----------------------------------------------------------

func (r *commentRepo) AddReaction(ctx context.Context, tx pgx.Tx, rx *CommentReaction) error {
	// Idempotent — toggling on twice is a no-op rather than a 409.
	_, err := tx.Exec(ctx, `
		INSERT INTO comment_reactions (tenant_id, comment_id, user_id, emoji)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (tenant_id, comment_id, user_id, emoji) DO NOTHING`,
		rx.TenantID, rx.CommentID, rx.UserID, rx.Emoji)
	return err
}

func (r *commentRepo) RemoveReaction(ctx context.Context, tx pgx.Tx, tenantID, commentID, userID uuid.UUID, emoji string) error {
	_, err := tx.Exec(ctx, `
		DELETE FROM comment_reactions
		 WHERE tenant_id = $1 AND comment_id = $2 AND user_id = $3 AND emoji = $4`,
		tenantID, commentID, userID, emoji)
	return err
}

func (r *commentRepo) ListReactions(ctx context.Context, tx pgx.Tx, tenantID, commentID uuid.UUID) ([]CommentReaction, error) {
	rows, err := tx.Query(ctx, `
		SELECT tenant_id, comment_id, user_id, emoji, created_at
		  FROM comment_reactions
		 WHERE tenant_id = $1 AND comment_id = $2`,
		tenantID, commentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CommentReaction
	for rows.Next() {
		var rx CommentReaction
		if err := rows.Scan(&rx.TenantID, &rx.CommentID, &rx.UserID, &rx.Emoji, &rx.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, rx)
	}
	return out, rows.Err()
}
