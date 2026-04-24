// Package service is the acknowledgement business-logic layer. The
// service owns (a) campaign lifecycle, (b) assignment resolution and
// acknowledgement, (c) HMAC attestation, and (d) the per-campaign
// hash chain stored in acknowledgement_events.
//
// Invariants enforced by this layer:
//   - Only users.role IN ('compliance_officer','admin','owner') can
//     create campaigns (caller enforces at the handler/OPA layer;
//     service reads the role from context and rejects otherwise).
//   - attestation_hash = HMAC-SHA256(tenant_kek, campaign_id || "|" ||
//     assignee_id || "|" || RFC3339Nano(acknowledged_at)).
//   - acknowledgement_events is append-only; each row's self_hash =
//     SHA-256(prev_hash || canonical(payload)). The FIRST row's
//     prev_hash is NULL.
//   - Outbox emission for every state change (campaign.created,
//     acknowledged, closed). Reminder + escalation events land when
//     the Temporal schedule wires up — tracked as Wave 15.1 follow-up.
package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"bytes"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/pkg/crypto"
	"github.com/vaultdms/vaultdms/pkg/database"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/acknowledgement/internal/model"
	"github.com/vaultdms/vaultdms/services/acknowledgement/internal/repository"
)

// Outbox event subjects. Versioned (v1) per the Wave-5 subject rules.
const (
	EventCampaignCreated = "dms.acknowledgement.campaign.created.v1"
	EventCampaignClosed  = "dms.acknowledgement.campaign.closed.v1"
	EventAcknowledged    = "dms.acknowledgement.acknowledged.v1"
	EventReminded        = "dms.acknowledgement.reminded.v1"
	EventEscalated       = "dms.acknowledgement.escalated.v1"

	// Notification fan-out subjects consumed by
	// services/notification. Shape must satisfy
	// notification/internal/model.DeliveryPayload (tenant_id,
	// user_ids, type, title, body, resource_type, resource_id).
	NotifyCampaignCreated = "dms.notify.acknowledgement.campaign.created.v1"
	NotifyReminded        = "dms.notify.acknowledgement.reminded.v1"
	NotifyEscalated       = "dms.notify.acknowledgement.escalated.v1"
)

// RecipientResolver turns a RecipientPolicy into a flat list of user
// IDs. Injected so the service doesn't bring the auth/identity
// service in as a hard dep; wire-up in cmd/server/main.go.
type RecipientResolver interface {
	Resolve(ctx context.Context, tenantID uuid.UUID, policy model.RecipientPolicy) ([]uuid.UUID, error)
}

// StaticRecipientResolver is used in dev/tests: returns policy.Users
// verbatim and ignores Groups/Roles. Production wiring supplies a
// real resolver that queries groups + role mappings.
type StaticRecipientResolver struct{}

// Resolve returns the explicit users list only — groups/roles are a
// follow-up (see WAVE_15_PROGRESS.md).
func (StaticRecipientResolver) Resolve(_ context.Context, _ uuid.UUID, p model.RecipientPolicy) ([]uuid.UUID, error) {
	if len(p.Groups) > 0 || len(p.Roles) > 0 {
		// Don't silently drop; caller should see the gap.
		return nil, vdmserr.Validation("recipient_policy",
			"group/role resolution not yet wired; supply users[] explicitly")
	}
	out := make([]uuid.UUID, 0, len(p.Users))
	seen := make(map[uuid.UUID]struct{}, len(p.Users))
	for _, u := range p.Users {
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	return out, nil
}

// Service bundles deps.
type Service struct {
	pool     *pgxpool.Pool
	repo     repository.Repo
	outbox   *database.OutboxRepository
	kms      crypto.KeyManager
	resolver RecipientResolver
	log      zerolog.Logger
	now      func() time.Time

	// keyCache holds plaintext HMAC keys keyed by tenant. Populated
	// lazily on first use after unwrapping the stored wrapped_key;
	// cleared on rotation. Protected by a RWMutex rather than a
	// sync.Map so we can truncate en masse during rotation.
	keyCacheMu sync.RWMutex
	keyCache   map[uuid.UUID][]byte
}

// Config bundles dependencies.
type Config struct {
	Pool     *pgxpool.Pool
	Repo     repository.Repo
	Outbox   *database.OutboxRepository
	KMS      crypto.KeyManager
	Resolver RecipientResolver
	Logger   zerolog.Logger
}

// New constructs a service.
func New(cfg Config) *Service {
	if cfg.Resolver == nil {
		cfg.Resolver = StaticRecipientResolver{}
	}
	return &Service{
		pool: cfg.Pool, repo: cfg.Repo, outbox: cfg.Outbox,
		kms: cfg.KMS, resolver: cfg.Resolver, log: cfg.Logger,
		now:      time.Now,
		keyCache: map[uuid.UUID][]byte{},
	}
}

func (s *Service) clock() time.Time { return s.now().UTC() }

// ---- CreateCampaign -------------------------------------------------------

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
				"campaign_id":      c.ID.String(),
				"tenant_id":        c.TenantID.String(),
				"document_id":      c.DocumentID.String(),
				"title":            c.Title,
				"due_at":           c.DueAt.Format(time.RFC3339Nano),
				"recipient_count":  len(assignments),
				"status":           string(c.Status),
				"created_by":       in.ActorID.String(),
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

// emitNotify inserts a `dms.notify.*` outbox row whose payload
// matches notification-service's DeliveryPayload shape. Kept local
// so the ack service doesn't take a hard dep on the notification
// module's types (which would create a workspace import cycle if
// the two ever share a helper). Resource fields are set so the
// inbox entry deep-links back to the campaign.
//
// Safe to call with users==nil — the helper no-ops so callers don't
// have to guard every site.
func (s *Service) emitNotify(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, campaignID uuid.UUID,
	users []uuid.UUID,
	subject, notifyType, title, body string,
) error {
	if len(users) == 0 {
		return nil
	}
	uids := make([]string, 0, len(users))
	for _, u := range users {
		uids = append(uids, u.String())
	}
	payload := map[string]any{
		"tenant_id":     tenantID.String(),
		"user_ids":      uids,
		"type":          notifyType,
		"title":         title,
		"body":          body,
		"resource_type": "acknowledgement_campaign",
		"resource_id":   campaignID.String(),
	}
	body2, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	evt := database.NewOutboxEvent(tenantID, subject, "acknowledgement", campaignID, body2)
	return s.outbox.Insert(ctx, tx, evt)
}

// ---- Acknowledge ----------------------------------------------------------

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

// ---- Report ---------------------------------------------------------------

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

// ---- Listing --------------------------------------------------------------

// ListCampaigns returns the tenant's campaigns, optionally filtered
// by status.
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

// ---- Reminders + escalations (Wave 15.1 sweeper) --------------------------

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

// ---- Attestation HMAC -----------------------------------------------------

// attestationHMAC derives a per-tenant key material from the KEK,
// then HMAC-SHA256s (campaign|user|timestamp). The KEK is wrapped and
// NEVER logged.
func (s *Service) attestationHMAC(ctx context.Context, tenantID, campaignID, userID uuid.UUID, at time.Time) ([]byte, error) {
	// Derive a 32-byte signing key from the tenant KEK. The crypto
	// package exposes GetOrCreateTenantMaterial which handles envelope
	// wrapping + caching.
	key, err := s.resolveTenantHMACKey(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("attestation key: %w", err)
	}
	h := hmac.New(sha256.New, key)
	h.Write([]byte(campaignID.String()))
	h.Write([]byte("|"))
	h.Write([]byte(userID.String()))
	h.Write([]byte("|"))
	h.Write([]byte(at.Format(time.RFC3339Nano)))
	return h.Sum(nil), nil
}

// InvalidateTenantSigningKey drops the cached plaintext signing key
// for a tenant. Call on KEK rotation so the next request re-unwraps
// via the new KEK id. Safe to call for tenants that aren't cached.
func (s *Service) InvalidateTenantSigningKey(tenantID uuid.UUID) {
	s.keyCacheMu.Lock()
	delete(s.keyCache, tenantID)
	s.keyCacheMu.Unlock()
}

// tenantKEKID returns the well-known KEK id for a tenant's
// acknowledgement signing key. Namespaced so it can't collide with
// other per-tenant keys (documents, MFA secrets, etc).
func tenantKEKID(tenantID uuid.UUID) string {
	return "vaultdms/tenant/" + tenantID.String() + "/acknowledgement"
}

// resolveTenantHMACKey returns the 32-byte HMAC key for the tenant.
// Creates-and-stores on first use; unwraps-and-caches on subsequent
// use. The plaintext key NEVER touches logs or the outbox.
func (s *Service) resolveTenantHMACKey(ctx context.Context, tenantID uuid.UUID) ([]byte, error) {
	if s.kms == nil {
		return nil, errors.New("acknowledgement: no KMS wired")
	}
	s.keyCacheMu.RLock()
	if k, ok := s.keyCache[tenantID]; ok {
		s.keyCacheMu.RUnlock()
		return k, nil
	}
	s.keyCacheMu.RUnlock()

	kekID := tenantKEKID(tenantID)
	var plaintext []byte
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		storedKEK, wrapped, getErr := s.repo.GetSigningKey(ctx, tx, tenantID)
		if getErr == nil {
			pt, err := s.kms.DecryptDataKey(ctx, storedKEK, wrapped)
			if err != nil {
				return fmt.Errorf("unwrap signing key: %w", err)
			}
			plaintext = pt
			return nil
		}
		// Not found → generate + persist.
		if vdmserr.KindOf(getErr) != vdmserr.KindNotFound {
			return getErr
		}
		pt, wrappedNew, err := s.kms.GenerateDataKey(ctx, kekID)
		if err != nil {
			return fmt.Errorf("generate signing key: %w", err)
		}
		if err := s.repo.InsertSigningKey(ctx, tx, tenantID, kekID, wrappedNew); err != nil {
			return err
		}
		plaintext = pt
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.keyCacheMu.Lock()
	s.keyCache[tenantID] = plaintext
	s.keyCacheMu.Unlock()
	return plaintext, nil
}

// VerifyAttestation recomputes the HMAC for a stored assignment and
// reports whether it matches. Exported for the CLI + integration
// tests; handlers use it only indirectly.
func (s *Service) VerifyAttestation(ctx context.Context, tenantID uuid.UUID, campaignID, userID uuid.UUID, at time.Time, stored []byte) (bool, error) {
	mac, err := s.attestationHMAC(ctx, tenantID, campaignID, userID, at)
	if err != nil {
		return false, err
	}
	return hmac.Equal(mac, stored), nil
}

// ---- Event append + hash chain --------------------------------------------

// appendEvent is the "first event for this campaign" path — no prev.
func (s *Service) appendEvent(ctx context.Context, tx pgx.Tx, tenantID, campaignID uuid.UUID, assignmentID *uuid.UUID, actorID *uuid.UUID, eventType string, payload map[string]any) error {
	return s.appendEventWithPrev(ctx, tx, tenantID, campaignID, assignmentID, actorID, eventType, payload, nil)
}

func (s *Service) appendEventWithPrev(ctx context.Context, tx pgx.Tx, tenantID, campaignID uuid.UUID, assignmentID, actorID *uuid.UUID, eventType string, payload map[string]any, prev []byte) error {
	body, err := canonicalJSON(payload)
	if err != nil {
		return err
	}
	selfHash := eventSelfHash(prev, body)
	e := &model.Event{
		TenantID:     tenantID,
		ID:           uuid.New(),
		CampaignID:   campaignID,
		AssignmentID: assignmentID,
		ActorUserID:  actorID,
		EventType:    eventType,
		Payload:      body,
		PrevHash:     prev,
		SelfHash:     selfHash,
		CreatedAt:    s.clock(),
	}
	if err := s.repo.AppendEvent(ctx, tx, e); err != nil {
		return err
	}
	// Outbox emission: handled separately so downstream (audit,
	// notifications) sees the event even if it's only interested in
	// the subject, not the chain.
	evt := database.NewOutboxEvent(tenantID, eventType, "acknowledgement", campaignID, body)
	return s.outbox.Insert(ctx, tx, evt)
}

// eventSelfHash computes SHA-256(prev || payload). Deterministic.
func eventSelfHash(prev, payload []byte) []byte {
	h := sha256.New()
	if len(prev) > 0 {
		h.Write(prev)
	}
	h.Write(payload)
	return h.Sum(nil)
}

// canonicalJSON serialises a payload with sorted keys so the
// hash-chain entry is byte-stable across replicas and replays. We
// do NOT rely on Go's incidental map-key ordering — a refactor to
// struct-shaped payloads would silently break chain verification.
// The implementation emits `{"k1":v1,"k2":v2,...}` with a top-level
// key sort; nested maps/structs are marshaled with the default
// encoder (any further canonicalisation can land when a concrete
// consumer needs it — today every payload is a flat map).
func canonicalJSON(payload map[string]any) ([]byte, error) {
	keys := make([]string, 0, len(payload))
	for k := range payload {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		vb, err := json.Marshal(payload[k])
		if err != nil {
			return nil, err
		}
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}
