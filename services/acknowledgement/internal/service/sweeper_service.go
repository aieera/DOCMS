package service

// sweeper_service.go — daily reminder + escalation pass invoked by
// the Temporal schedule. Rules come straight from the Wave 15.1
// brief; see SweepReminders for the canonical statement.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/vaultdms/vaultdms/pkg/database"
)

// SweepRemindersResult is what SweepReminders returns. Each counter
// is per this invocation, not cumulative.
type SweepRemindersResult struct {
	Reminded  int
	Escalated int
}

// SweepReminders finds due / overdue assignments for a tenant and
// emits the corresponding dms.acknowledgement.reminded.v1 /
// escalated.v1 events via the outbox. The worker's Temporal
// schedule runs this daily at 09:00 UTC per tenant.
//
// Rules per Wave 15.1 brief:
//   - remind if acknowledged_at IS NULL AND (due_at - 3d) <= now
//     AND reminded_at IS NULL OR reminded_at < today (one per day)
//   - escalate if acknowledged_at IS NULL AND now >= due_at + 7d
//     AND escalated_at IS NULL (one-shot)
//
// This method does NOT send emails — notification service owns
// that; the event subjects above are what notification binds to.
func (s *Service) SweepReminders(ctx context.Context, tenantID uuid.UUID) (SweepRemindersResult, error) {
	var out SweepRemindersResult
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		now := s.clock()

		// --- Reminders --------------------------------------------------
		remindRows, err := tx.Query(ctx, `
			UPDATE acknowledgement_assignments a
			   SET reminded_count = reminded_count + 1,
			       reminded_at    = $2
			 WHERE tenant_id = $1
			   AND acknowledged_at IS NULL
			   AND id IN (
			     SELECT a2.id
			       FROM acknowledgement_assignments a2
			       JOIN acknowledgement_campaigns   c
			         ON c.tenant_id = a2.tenant_id AND c.id = a2.campaign_id
			      WHERE a2.tenant_id = $1
			        AND a2.acknowledged_at IS NULL
			        AND (c.due_at - interval '3 days') <= $2
			        AND (a2.reminded_at IS NULL OR a2.reminded_at::date < $2::date)
			      LIMIT 500
			   )
			RETURNING a.id, a.campaign_id, a.assignee_user_id`,
			tenantID, now)
		if err != nil {
			return err
		}
		type pair struct {
			AssignmentID uuid.UUID
			CampaignID   uuid.UUID
			UserID       uuid.UUID
		}
		var reminders []pair
		for remindRows.Next() {
			var p pair
			if err := remindRows.Scan(&p.AssignmentID, &p.CampaignID, &p.UserID); err != nil {
				remindRows.Close()
				return err
			}
			reminders = append(reminders, p)
		}
		remindRows.Close()

		for _, r := range reminders {
			aID := r.AssignmentID
			body, err := json.Marshal(map[string]any{
				"campaign_id":   r.CampaignID.String(),
				"assignment_id": aID.String(),
				"tenant_id":     tenantID.String(),
				"at":            now.Format(time.RFC3339Nano),
			})
			if err != nil {
				return fmt.Errorf("marshal reminded payload: %w", err)
			}
			evt := database.NewOutboxEvent(tenantID, EventReminded, "acknowledgement", r.CampaignID, body)
			if err := s.outbox.Insert(ctx, tx, evt); err != nil {
				return err
			}
		}
		// Notification fan-out for reminders. Group by campaign so
		// the notification service receives one delivery row per
		// (campaign, user-set) pair rather than N rows per user —
		// keeps outbox volume proportional to campaigns, not users.
		if len(reminders) > 0 {
			byCampaign := map[uuid.UUID][]uuid.UUID{}
			for _, r := range reminders {
				byCampaign[r.CampaignID] = append(byCampaign[r.CampaignID], r.UserID)
			}
			for campaignID, users := range byCampaign {
				if err := s.emitNotify(
					ctx, tx, tenantID, campaignID, users,
					NotifyReminded,
					"acknowledgement.reminded",
					"Reminder: acknowledgement due soon",
					"You have an outstanding acknowledgement; please complete it before the due date.",
				); err != nil {
					return err
				}
			}
		}
		out.Reminded = len(reminders)

		// --- Escalations ------------------------------------------------
		escRows, err := tx.Query(ctx, `
			UPDATE acknowledgement_assignments a
			   SET escalated_at = $2
			 WHERE tenant_id = $1
			   AND acknowledged_at IS NULL
			   AND escalated_at IS NULL
			   AND id IN (
			     SELECT a2.id
			       FROM acknowledgement_assignments a2
			       JOIN acknowledgement_campaigns   c
			         ON c.tenant_id = a2.tenant_id AND c.id = a2.campaign_id
			      WHERE a2.tenant_id = $1
			        AND a2.acknowledged_at IS NULL
			        AND a2.escalated_at IS NULL
			        AND $2 >= (c.due_at + interval '7 days')
			      LIMIT 500
			   )
			RETURNING a.id, a.campaign_id, a.assignee_user_id`,
			tenantID, now)
		if err != nil {
			return err
		}
		type etriple struct {
			AssignmentID, CampaignID, UserID uuid.UUID
		}
		var escs []etriple
		for escRows.Next() {
			var e etriple
			if err := escRows.Scan(&e.AssignmentID, &e.CampaignID, &e.UserID); err != nil {
				escRows.Close()
				return err
			}
			escs = append(escs, e)
		}
		escRows.Close()

		for _, e := range escs {
			body, err := json.Marshal(map[string]any{
				"campaign_id":   e.CampaignID.String(),
				"assignment_id": e.AssignmentID.String(),
				"user_id":       e.UserID.String(),
				"tenant_id":     tenantID.String(),
				"at":            now.Format(time.RFC3339Nano),
			})
			if err != nil {
				return fmt.Errorf("marshal escalated payload: %w", err)
			}
			evt := database.NewOutboxEvent(tenantID, EventEscalated, "acknowledgement", e.CampaignID, body)
			if err := s.outbox.Insert(ctx, tx, evt); err != nil {
				return err
			}
		}
		// Notification fan-out for escalations. Unlike reminders,
		// escalation also CCs compliance-officer + manager — that
		// resolution lives in the notification service's per-type
		// routing config, so this emitter just carries the user set
		// and the service-side router handles the expansion.
		if len(escs) > 0 {
			byCampaign := map[uuid.UUID][]uuid.UUID{}
			for _, e := range escs {
				byCampaign[e.CampaignID] = append(byCampaign[e.CampaignID], e.UserID)
			}
			for campaignID, users := range byCampaign {
				if err := s.emitNotify(
					ctx, tx, tenantID, campaignID, users,
					NotifyEscalated,
					"acknowledgement.escalated",
					"Escalation: overdue acknowledgement",
					"An acknowledgement is more than 7 days overdue and has been escalated.",
				); err != nil {
					return err
				}
			}
		}
		out.Escalated = len(escs)
		return nil
	})
	return out, err
}
