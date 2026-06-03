// ADR 0066 — threaded comments + @mentions + reactions service.
//
// Mirrors the annotation service pattern in this package:
//   - mustCaller for identity
//   - requirePermission for read/edit on the parent document
//   - withTenantTx for RLS-scoped writes
//   - outbox events for collaboration WS fanout + audit + mention
//     notifications
//
// Mention parsing: bodies use the `@[Display Name](user-uuid)`
// shape. Anything that matches the regex below is a mention; the
// service emits dms.notify.comment.mention.v1 for each unique
// mentioned user_id (deduped, self-mention dropped, only mentions
// in the diff on edit).
package service

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/repository"
)

// mentionRE matches `@[Display Name](uuid)`. The display label is
// non-empty and excludes a literal `]`; the uuid follows the
// canonical 8-4-4-4-12 form.
var mentionRE = regexp.MustCompile(`@\[([^\]]+)\]\(([0-9a-fA-F\-]{36})\)`)

// CreateCommentInput is the service-layer shape for both top-level
// comments and replies. ParentID = nil means top-level.
type CreateCommentInput struct {
	DocumentID uuid.UUID
	VersionID  *uuid.UUID
	ParentID   *uuid.UUID
	Body       string
}

// CommentView is what handlers return to the client. Wraps the repo
// row and includes the rolled-up reactions (emoji → user_ids list)
// when the caller asks for them.
type CommentView struct {
	repository.Comment
	Reactions []ReactionAggregate `json:"reactions,omitempty"`
}

// ReactionAggregate rolls reactions up by emoji for the FE so it
// doesn't have to group N rows itself.
type ReactionAggregate struct {
	Emoji string      `json:"emoji"`
	Count int         `json:"count"`
	Users []uuid.UUID `json:"users"`
}

// CreateComment persists a new comment + emits the create event +
// emits a mention event per unique mentioned user.
func (s *DocumentService) CreateComment(ctx context.Context, in *CreateCommentInput) (*repository.Comment, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if in.DocumentID == uuid.Nil {
		return nil, vdmserr.Validation("document_id", "required")
	}
	body := strings.TrimSpace(in.Body)
	if body == "" || len(body) > 10000 {
		return nil, vdmserr.Validation("body", "1..10000 chars")
	}

	id, err := newExternalID()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	c := &repository.Comment{
		TenantID:        tenantID,
		ID:              id,
		DocumentID:      in.DocumentID,
		VersionID:       in.VersionID,
		ParentCommentID: in.ParentID,
		AuthorID:        userID,
		Body:            body,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	mentioned := parseMentions(body)
	delete(mentioned, userID) // self-mention drops out

	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		doc, err := s.repos.Documents.GetByID(ctx, tx, tenantID, in.DocumentID)
		if err != nil {
			return err
		}
		if doc.DeletedAt != nil {
			return vdmserr.ErrNotFound
		}
		if err := s.requirePermission(ctx, userID, "view", "document", doc.ID, map[string]any{
			"workspace_id": doc.WorkspaceID.String(),
		}); err != nil {
			return err
		}
		// Reply path: parent must exist on the same document and not
		// be a reply itself (no nested replies — flat 2-level shape).
		if in.ParentID != nil {
			p, err := s.repos.Comments.GetByID(ctx, tx, tenantID, *in.ParentID)
			if err != nil {
				return err
			}
			if p.DocumentID != in.DocumentID {
				return vdmserr.Validation("parent_id", "parent belongs to a different document")
			}
			if p.ParentCommentID != nil {
				return vdmserr.Validation("parent_id", "replies cannot have replies — use the thread root")
			}
		}
		if err := s.repos.Comments.Create(ctx, tx, c); err != nil {
			return err
		}
		// Outbox: create + per-mention notify. Both ride the same tx
		// so a failure rolls back the comment too.
		if err := s.emitCommentEvent(ctx, tx, "dms.comment.created.v1", c, userID, doc.ID); err != nil {
			return err
		}
		for mid := range mentioned {
			if err := s.emitMention(ctx, tx, c, userID, mid, doc.ID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return c, nil
}

// GetCommentByID is the read used by the reply handler to discover
// a parent comment's document_id before calling CreateComment.
// Permission: view on the document.
func (s *DocumentService) GetCommentByID(ctx context.Context, id uuid.UUID) (*repository.Comment, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var out *repository.Comment
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		c, err := s.repos.Comments.GetByID(ctx, tx, tenantID, id)
		if err != nil {
			return err
		}
		doc, err := s.repos.Documents.GetByID(ctx, tx, tenantID, c.DocumentID)
		if err != nil {
			return err
		}
		if err := s.requirePermission(ctx, userID, "view", "document", doc.ID, map[string]any{
			"workspace_id": doc.WorkspaceID.String(),
		}); err != nil {
			return err
		}
		out = c
		return nil
	})
	return out, err
}

// ListComments returns every non-deleted comment on the document.
// includeResolved=false hides resolved threads (root + replies).
func (s *DocumentService) ListComments(ctx context.Context, documentID uuid.UUID, includeResolved bool) ([]repository.Comment, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var out []repository.Comment
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		doc, err := s.repos.Documents.GetByID(ctx, tx, tenantID, documentID)
		if err != nil {
			return err
		}
		if doc.DeletedAt != nil {
			return vdmserr.ErrNotFound
		}
		if err := s.requirePermission(ctx, userID, "view", "document", doc.ID, map[string]any{
			"workspace_id": doc.WorkspaceID.String(),
		}); err != nil {
			return err
		}
		out, err = s.repos.Comments.ListByDocument(ctx, tx, tenantID, documentID, includeResolved)
		return err
	})
	return out, err
}

// UpdateComment edits the body. Author-only (admins go through
// SoftDeleteComment if they want to take down a row). Re-parses
// mentions and emits notifications for newly-added ones only.
func (s *DocumentService) UpdateComment(ctx context.Context, id uuid.UUID, body string) (*repository.Comment, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	body = strings.TrimSpace(body)
	if body == "" || len(body) > 10000 {
		return nil, vdmserr.Validation("body", "1..10000 chars")
	}
	var updated *repository.Comment
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		existing, err := s.repos.Comments.GetByID(ctx, tx, tenantID, id)
		if err != nil {
			return err
		}
		if existing.AuthorID != userID {
			return vdmserr.Forbidden("only the author may edit a comment")
		}
		oldMentions := parseMentions(existing.Body)
		newMentions := parseMentions(body)
		delete(newMentions, userID)

		if err := s.repos.Comments.UpdateBody(ctx, tx, tenantID, id, body); err != nil {
			return err
		}
		existing.Body = body
		existing.UpdatedAt = time.Now().UTC()
		updated = existing

		if err := s.emitCommentEvent(ctx, tx, "dms.comment.updated.v1", updated, userID, existing.DocumentID); err != nil {
			return err
		}
		// Notify only the newly-mentioned users — re-pinging old
		// mentions on every typo fix is annoying.
		for mid := range newMentions {
			if _, alreadyMentioned := oldMentions[mid]; alreadyMentioned {
				continue
			}
			if err := s.emitMention(ctx, tx, updated, userID, mid, existing.DocumentID); err != nil {
				return err
			}
		}
		return nil
	})
	return updated, err
}

// SoftDeleteComment soft-deletes a comment. Author or admin/owner.
func (s *DocumentService) SoftDeleteComment(ctx context.Context, id uuid.UUID) error {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	role := callerRole(ctx)
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		existing, err := s.repos.Comments.GetByID(ctx, tx, tenantID, id)
		if err != nil {
			return err
		}
		if existing.AuthorID != userID && role != "admin" && role != "owner" {
			return vdmserr.Forbidden("only the author or an admin may delete a comment")
		}
		if err := s.repos.Comments.SoftDelete(ctx, tx, tenantID, id); err != nil {
			return err
		}
		return s.emitCommentEvent(ctx, tx, "dms.comment.deleted.v1", existing, userID, existing.DocumentID)
	})
}

// SetCommentResolved flips the resolve flag on a thread root.
// Replies cannot be resolved independently (rejected here).
func (s *DocumentService) SetCommentResolved(ctx context.Context, id uuid.UUID, resolved bool) error {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		existing, err := s.repos.Comments.GetByID(ctx, tx, tenantID, id)
		if err != nil {
			return err
		}
		if existing.ParentCommentID != nil {
			return vdmserr.Validation("comment", "replies cannot be resolved — resolve the thread root")
		}
		// Authority: author OR users with edit on the document.
		if existing.AuthorID != userID {
			doc, err := s.repos.Documents.GetByID(ctx, tx, tenantID, existing.DocumentID)
			if err != nil {
				return err
			}
			if err := s.requirePermission(ctx, userID, "edit", "document", doc.ID, map[string]any{
				"workspace_id": doc.WorkspaceID.String(),
			}); err != nil {
				return err
			}
		}
		if err := s.repos.Comments.SetResolved(ctx, tx, tenantID, id, userID, resolved); err != nil {
			return err
		}
		subject := "dms.comment.resolved.v1"
		if !resolved {
			subject = "dms.comment.updated.v1" // unresolve flows through updated
		}
		existing.IsResolved = resolved
		return s.emitCommentEvent(ctx, tx, subject, existing, userID, existing.DocumentID)
	})
}

// AddReaction toggles ON. Idempotent.
func (s *DocumentService) AddReaction(ctx context.Context, commentID uuid.UUID, emoji string) error {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	emoji = strings.TrimSpace(emoji)
	if emoji == "" || len(emoji) > 32 {
		return vdmserr.Validation("emoji", "1..32 chars")
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		existing, err := s.repos.Comments.GetByID(ctx, tx, tenantID, commentID)
		if err != nil {
			return err
		}
		// Reuse view-permission for reactions — same bar as reading
		// the comment.
		doc, err := s.repos.Documents.GetByID(ctx, tx, tenantID, existing.DocumentID)
		if err != nil {
			return err
		}
		if err := s.requirePermission(ctx, userID, "view", "document", doc.ID, map[string]any{
			"workspace_id": doc.WorkspaceID.String(),
		}); err != nil {
			return err
		}
		if err := s.repos.Comments.AddReaction(ctx, tx, &repository.CommentReaction{
			TenantID: tenantID, CommentID: commentID, UserID: userID, Emoji: emoji,
		}); err != nil {
			return err
		}
		return s.emitReactionEvent(ctx, tx, tenantID, commentID, existing.DocumentID, userID, emoji, "added")
	})
}

// RemoveReaction toggles OFF. Idempotent — removing a reaction the
// user never added is a no-op.
func (s *DocumentService) RemoveReaction(ctx context.Context, commentID uuid.UUID, emoji string) error {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		existing, err := s.repos.Comments.GetByID(ctx, tx, tenantID, commentID)
		if err != nil {
			return err
		}
		if err := s.repos.Comments.RemoveReaction(ctx, tx, tenantID, commentID, userID, emoji); err != nil {
			return err
		}
		return s.emitReactionEvent(ctx, tx, tenantID, commentID, existing.DocumentID, userID, emoji, "removed")
	})
}

// ListReactions returns every reaction on a comment, ungrouped. The
// handler aggregates by emoji before sending to the FE.
func (s *DocumentService) ListReactions(ctx context.Context, commentID uuid.UUID) ([]repository.CommentReaction, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var out []repository.CommentReaction
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		out, err = s.repos.Comments.ListReactions(ctx, tx, tenantID, commentID)
		return err
	})
	return out, err
}

// ---- helpers ------------------------------------------------------------

// parseMentions extracts the unique user_id set from a comment body.
// Returns map[uuid.UUID]struct{} so callers can subtract sets cheaply.
func parseMentions(body string) map[uuid.UUID]struct{} {
	out := map[uuid.UUID]struct{}{}
	for _, m := range mentionRE.FindAllStringSubmatch(body, -1) {
		uid, err := uuid.Parse(m[2])
		if err != nil {
			continue
		}
		out[uid] = struct{}{}
	}
	return out
}

// emitCommentEvent inserts an outbox row for one of the
// dms.comment.{created,updated,resolved,deleted}.v1 subjects.
func (s *DocumentService) emitCommentEvent(ctx context.Context, tx pgx.Tx, subject string, c *repository.Comment, actorID, documentID uuid.UUID) error {
	evt, err := model.NewOutboxEvent(c.TenantID, subject, "comment", c.ID, map[string]any{
		"tenant_id":         c.TenantID.String(),
		"document_id":       documentID.String(),
		"comment_id":        c.ID.String(),
		"parent_comment_id": uuidPtrToString(c.ParentCommentID),
		"author_id":         c.AuthorID.String(),
		"actor_id":          actorID.String(),
		"is_resolved":       c.IsResolved,
		"at":                time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	return s.repos.Outbox.Insert(ctx, tx, evt)
}

// emitMention emits dms.notify.comment.mention.v1 with the
// mentioned user as the recipient.
//
// Payload shape MUST match notification.model.DeliveryPayload —
// the consumer subscribes to `dms.notify.>` and drops messages
// whose user_ids slice is empty or whose title/body are missing.
// Earlier versions of this function used a `mentioned_user_id`
// field that the consumer silently Term'd, so emails never landed.
func (s *DocumentService) emitMention(ctx context.Context, tx pgx.Tx, c *repository.Comment, byUserID, mentionedUserID, documentID uuid.UUID) error {
	excerpt := c.Body
	if len(excerpt) > 200 {
		excerpt = excerpt[:200] + "…"
	}
	evt, err := model.NewOutboxEvent(c.TenantID, "dms.notify.comment.mention.v1", "comment", c.ID, map[string]any{
		// notification service required fields:
		"tenant_id":     c.TenantID.String(),
		"user_ids":      []string{mentionedUserID.String()},
		"type":          "comment.mention",
		"title":         "You were mentioned in a comment",
		"body":          excerpt,
		"resource_type": "comment",
		"resource_id":   c.ID.String(),
		// extras the audit log + downstream consumers care about:
		"document_id":  documentID.String(),
		"comment_id":   c.ID.String(),
		"by_user_id":   byUserID.String(),
		"at":           time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	return s.repos.Outbox.Insert(ctx, tx, evt)
}

// emitReactionEvent emits dms.comment.reaction.v1.
func (s *DocumentService) emitReactionEvent(ctx context.Context, tx pgx.Tx, tenantID, commentID, documentID, userID uuid.UUID, emoji, action string) error {
	evt, err := model.NewOutboxEvent(tenantID, "dms.comment.reaction.v1", "comment", commentID, map[string]any{
		"tenant_id":   tenantID.String(),
		"document_id": documentID.String(),
		"comment_id":  commentID.String(),
		"user_id":     userID.String(),
		"emoji":       emoji,
		"action":      action, // added | removed
		"at":          time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	return s.repos.Outbox.Insert(ctx, tx, evt)
}

func uuidPtrToString(u *uuid.UUID) string {
	if u == nil {
		return ""
	}
	return u.String()
}
