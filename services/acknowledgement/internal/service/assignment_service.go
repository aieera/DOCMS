package service

// assignment_service.go — per-user acknowledgement flow + report.

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/vaultdms/vaultdms/pkg/database"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/acknowledgement/internal/model"
)

// AcknowledgeInput is what the ack endpoint passes through.
type AcknowledgeInput struct {
	TenantID     uuid.UUID
	ActorID      uuid.UUID
	AssignmentID uuid.UUID
	IPAddress    string
	UserAgent    string
	Comment      string
}

// Acknowledge stamps the assignment, computes + stores the HMAC,
// appends the chain event, and emits the outbox record.
func (s *Service) Acknowledge(ctx context.Context, in AcknowledgeInput) (*model.Assignment, error) {
	var out *model.Assignment
	err := database.WithTenantTx(ctx, s.pool, in.TenantID, func(tx pgx.Tx) error {
		a, err := s.repo.GetAssignment(ctx, tx, in.TenantID, in.AssignmentID)
		if err != nil {
			return err
		}
		if a.AssigneeUserID != in.ActorID {
			// Policy enforcement is in the handler/OPA layer; this
			// is defence-in-depth so a leaked token can't ack on
			// behalf of another user.
			return vdmserr.ErrForbidden
		}
		if a.AcknowledgedAt != nil {
			return vdmserr.Conflict("already acknowledged")
		}

		now := s.clock()
		mac, err := s.attestationHMAC(ctx, in.TenantID, a.CampaignID, a.AssigneeUserID, now)
		if err != nil {
			return err
		}

		a.AcknowledgedAt = &now
		a.IPAddress = in.IPAddress
		a.UserAgent = in.UserAgent
		a.Comment = in.Comment
		a.AttestationHash = mac
		if err := s.repo.MarkAcknowledged(ctx, tx, a); err != nil {
			return err
		}

		head, err := s.repo.ChainHeadForCampaign(ctx, tx, in.TenantID, a.CampaignID)
		if err != nil {
			return err
		}
		if err := s.appendEventWithPrev(ctx, tx, in.TenantID, a.CampaignID, &a.ID, &in.ActorID,
			EventAcknowledged, map[string]any{
				"campaign_id":      a.CampaignID.String(),
				"assignment_id":    a.ID.String(),
				"user_id":          a.AssigneeUserID.String(),
				"tenant_id":        in.TenantID.String(),
				"acknowledged_at":  now.Format(time.RFC3339Nano),
				"attestation_hash": fmt.Sprintf("%x", mac),
			}, head.SelfHash); err != nil {
			return err
		}
		out = a
		return nil
	})
	return out, err
}

// MyPending returns assignments owed by the actor that are not yet ack'd.
func (s *Service) MyPending(ctx context.Context, tenantID, userID uuid.UUID) ([]model.Assignment, error) {
	var out []model.Assignment
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		list, err := s.repo.ListPendingForUser(ctx, tx, tenantID, userID)
		if err != nil {
			return err
		}
		out = list
		return nil
	})
	return out, err
}

// Report returns the rollup for a campaign.
func (s *Service) Report(ctx context.Context, tenantID, campaignID uuid.UUID) (*model.Report, error) {
	var rep model.Report
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		c, err := s.repo.GetCampaign(ctx, tx, tenantID, campaignID)
		if err != nil {
			return err
		}
		total, acked, overdue, escalated, err := s.repo.ReportCounts(ctx, tx, tenantID, campaignID, s.clock())
		if err != nil {
			return err
		}
		rate := 0.0
		if total > 0 {
			rate = float64(acked) / float64(total)
		}
		rep = model.Report{
			CampaignID:   campaignID,
			Total:        total,
			Acknowledged: acked,
			Overdue:      overdue,
			Escalated:    escalated,
			AckRate:      rate,
			DueAt:        c.DueAt,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &rep, nil
}
