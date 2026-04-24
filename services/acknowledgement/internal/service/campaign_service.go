package service

// campaign_service.go — campaign CRUD + lifecycle. No state is held
// here beyond what Service carries; the split is by seam, not by
// struct.

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/vaultdms/vaultdms/pkg/database"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/acknowledgement/internal/model"
)

// CreateCampaignInput is the validated shape.
type CreateCampaignInput struct {
	TenantID        uuid.UUID
	ActorID         uuid.UUID
	DocumentID      uuid.UUID
	VersionID       *uuid.UUID
	Title           string
	BodyMD          string
	DueAt           time.Time
	RecipientPolicy model.RecipientPolicy
	// Activate=true resolves the recipient policy and inserts
	// assignments inside the same tx; status is 'active'. When
	// false the campaign lands in 'draft' and assignments are
	// materialised on a later PATCH to 'active'.
	Activate bool
}

// CreateCampaign persists the campaign + (if Activate) the assignment
// rows + emits dms.acknowledgement.campaign.created.v1.
func (s *Service) CreateCampaign(ctx context.Context, in CreateCampaignInput) (*model.Campaign, []model.Assignment, error) {
	if in.Title == "" {
		return nil, nil, vdmserr.Validation("title", "required")
	}
	if in.DueAt.IsZero() || in.DueAt.Before(s.clock()) {
		return nil, nil, vdmserr.Validation("due_at", "must be a future timestamp")
	}

	c := &model.Campaign{
		TenantID:        in.TenantID,
		ID:              uuid.New(),
		DocumentID:      in.DocumentID,
		VersionID:       in.VersionID,
		Title:           in.Title,
		BodyMD:          in.BodyMD,
		DueAt:           in.DueAt.UTC(),
		CreatedByUserID: in.ActorID,
		CreatedAt:       s.clock(),
		UpdatedAt:       s.clock(),
		Status:          model.StatusDraft,
		RecipientPolicy: in.RecipientPolicy,
	}
	if in.Activate {
		c.Status = model.StatusActive
	}

	var assignments []model.Assignment
	err := database.WithTenantTx(ctx, s.pool, in.TenantID, func(tx pgx.Tx) error {
		if err := s.repo.InsertCampaign(ctx, tx, c); err != nil {
			return err
		}
		if in.Activate {
			users, err := s.resolver.Resolve(ctx, in.TenantID, in.RecipientPolicy)
			if err != nil {
				return err
			}
			if len(users) == 0 {
				return vdmserr.Validation("recipient_policy", "resolved to zero recipients")
			}
			if _, err := s.repo.BulkInsertAssignments(ctx, tx, in.TenantID, c.ID, users); err != nil {
				return err
			}
			list, err := s.repo.ListAssignmentsByCampaign(ctx, tx, in.TenantID, c.ID)
			if err != nil {
				return err
			}
			assignments = list
		}
		// Chain: first event for the campaign — prev_hash nil.
		if err := s.appendEvent(ctx, tx, c.TenantID, c.ID, nil, &in.ActorID,
			EventCampaignCreated, map[string]any{
				"campaign_id":     c.ID.String(),
				"tenant_id":       c.TenantID.String(),
				"document_id":     c.DocumentID.String(),
				"title":           c.Title,
				"due_at":          c.DueAt.Format(time.RFC3339Nano),
				"recipient_count": len(assignments),
				"status":          string(c.Status),
				"created_by":      in.ActorID.String(),
			}); err != nil {
			return err
		}
		// Fan-out one notification-service delivery payload for the
		// freshly-activated campaign. The notification consumer
		// handles the per-user email + inbox write inside the 60 s
		// DoD window. Draft campaigns don't notify — activation is
		// the user-visible milestone.
		if in.Activate && len(assignments) > 0 {
			users := make([]uuid.UUID, 0, len(assignments))
			for _, a := range assignments {
				users = append(users, a.AssigneeUserID)
			}
			if err := s.emitNotify(
				ctx, tx, c.TenantID, c.ID, users,
				NotifyCampaignCreated,
				"acknowledgement.campaign.created",
				"Action required: "+c.Title,
				"A document requires your acknowledgement by "+
					c.DueAt.Format("2006-01-02")+".",
			); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return c, assignments, nil
}

// GetCampaign returns a single campaign by id. O(1) via the repo's
// indexed (tenant_id, id) lookup — the handler previously linear-
// scanned the full tenant list which was an obvious DoS foothold.
func (s *Service) GetCampaign(ctx context.Context, tenantID, campaignID uuid.UUID) (*model.Campaign, error) {
	var out *model.Campaign
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		c, err := s.repo.GetCampaign(ctx, tx, tenantID, campaignID)
		if err != nil {
			return err
		}
		out = c
		return nil
	})
	return out, err
}

// ListCampaigns returns the tenant's campaigns, optionally filtered
// by status.
func (s *Service) ListCampaigns(ctx context.Context, tenantID uuid.UUID, status model.Status) ([]model.Campaign, error) {
	var out []model.Campaign
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		list, err := s.repo.ListCampaigns(ctx, tx, tenantID, status)
		if err != nil {
			return err
		}
		out = list
		return nil
	})
	return out, err
}

// CloseCampaign flips status and emits the event. Idempotent.
func (s *Service) CloseCampaign(ctx context.Context, tenantID, actorID, campaignID uuid.UUID) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		c, err := s.repo.GetCampaign(ctx, tx, tenantID, campaignID)
		if err != nil {
			return err
		}
		if c.Status == model.StatusClosed || c.Status == model.StatusArchived {
			return nil
		}
		now := s.clock()
		if err := s.repo.UpdateCampaignStatus(ctx, tx, tenantID, campaignID, model.StatusClosed, &now); err != nil {
			return err
		}
		head, err := s.repo.ChainHeadForCampaign(ctx, tx, tenantID, campaignID)
		if err != nil {
			return err
		}
		return s.appendEventWithPrev(ctx, tx, tenantID, campaignID, nil, &actorID,
			EventCampaignClosed, map[string]any{
				"campaign_id": campaignID.String(),
				"tenant_id":   tenantID.String(),
				"closed_by":   actorID.String(),
				"closed_at":   now.Format(time.RFC3339Nano),
			}, head.SelfHash)
	})
}
