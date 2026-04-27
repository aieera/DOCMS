package compliance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/vaultdms/vaultdms/pkg/database"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
)

// Custodian is one user attached to a hold. Notification + ack
// timestamps drive the /legal-holds/my inbox + the
// dms.notify.legalhold.* fan-out.
type Custodian struct {
	ID             uuid.UUID  `json:"id"`
	HoldID         uuid.UUID  `json:"hold_id"`
	UserID         uuid.UUID  `json:"user_id"`
	NotifiedAt     *time.Time `json:"notified_at,omitempty"`
	AcknowledgedAt *time.Time `json:"acknowledged_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

// AddCustodians inserts one row per user. Idempotent on the
// (tenant_id, hold_id, user_id) unique key — re-adding a user is a
// no-op rather than a 409. Emits one chained custodian_added event
// per inserted row + one outbound dms.notify.legalhold.applied.v1
// per user so the notification service can deliver to the configured
// channels (in-app + email today).
func (s *HoldsService) AddCustodians(
	ctx context.Context,
	tenantID, holdID, actorID uuid.UUID,
	userIDs []uuid.UUID,
) ([]Custodian, error) {
	if len(userIDs) == 0 {
		return nil, vdmserr.Validation("user_ids", "at least one required")
	}
	out := make([]Custodian, 0, len(userIDs))
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		// Confirm hold exists + is active before binding custodians
		// to it. A released hold can't gain new custodians.
		var active bool
		if err := tx.QueryRow(ctx,
			`SELECT is_active FROM legal_holds WHERE tenant_id = $1 AND id = $2`,
			tenantID, holdID,
		).Scan(&active); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return vdmserr.ErrNotFound
			}
			return fmt.Errorf("hold lookup: %w", err)
		}
		if !active {
			return vdmserr.Conflict("cannot add custodians to a released hold")
		}

		for _, uid := range userIDs {
			id, _ := uuid.NewV7()
			now := time.Now().UTC()
			_, err := tx.Exec(ctx, `
				INSERT INTO legal_hold_custodians
				    (tenant_id, id, hold_id, user_id, notified_at, created_at)
				VALUES ($1, $2, $3, $4, $5, $5)
				ON CONFLICT (tenant_id, hold_id, user_id) DO NOTHING
			`, tenantID, id, holdID, uid, now)
			if err != nil {
				return fmt.Errorf("custodian insert: %w", err)
			}

			c := Custodian{
				ID: id, HoldID: holdID, UserID: uid,
				NotifiedAt: &now, CreatedAt: now,
			}
			out = append(out, c)

			if _, err := AppendEvent(ctx, tx, tenantID, holdID, &actorID,
				EventCustodianAdded, map[string]any{
					"custodian_id": id.String(),
					"user_id":      uid.String(),
				}); err != nil {
				return err
			}
		}

		// Notification fan-out — one outbox row carrying the full
		// user_ids array. The notification service's dms.notify.>
		// consumer fans out to each user's enabled channels.
		userIDStrs := make([]string, len(userIDs))
		for i, u := range userIDs {
			userIDStrs[i] = u.String()
		}
		return emitLegalHoldNotify(ctx, tx, tenantID, holdID, actorID,
			"dms.notify.legalhold.applied.v1", userIDStrs,
			"You are a custodian on a legal hold",
			"You must preserve documents related to this matter. Acknowledge in the legal-hold inbox.")
	})
	return out, err
}

// AcknowledgeCustodian stamps acknowledged_at for the (hold, user) row.
// Only the custodian themselves may call it (handler enforces with
// requireUser); the hold owner cannot ack on a custodian's behalf.
func (s *HoldsService) AcknowledgeCustodian(
	ctx context.Context,
	tenantID, holdID, userID uuid.UUID,
) (*Custodian, error) {
	var c Custodian
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var (
			notified *time.Time
			acked    *time.Time
			id       uuid.UUID
			created  time.Time
		)
		now := time.Now().UTC()
		err := tx.QueryRow(ctx, `
			UPDATE legal_hold_custodians
			   SET acknowledged_at = COALESCE(acknowledged_at, $1)
			 WHERE tenant_id = $2 AND hold_id = $3 AND user_id = $4
			RETURNING id, notified_at, acknowledged_at, created_at
		`, now, tenantID, holdID, userID).Scan(&id, &notified, &acked, &created)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return vdmserr.ErrNotFound
			}
			return fmt.Errorf("ack update: %w", err)
		}
		c = Custodian{
			ID: id, HoldID: holdID, UserID: userID,
			NotifiedAt: notified, AcknowledgedAt: acked, CreatedAt: created,
		}
		_, err = AppendEvent(ctx, tx, tenantID, holdID, &userID,
			EventCustodianAcknowledged, map[string]any{
				"user_id": userID.String(),
				"at":      now.Format(time.RFC3339),
			})
		return err
	})
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// ListCustodians returns every custodian on a hold for the admin tab.
func (s *HoldsService) ListCustodians(ctx context.Context, tenantID, holdID uuid.UUID) ([]Custodian, error) {
	var out []Custodian
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, hold_id, user_id, notified_at, acknowledged_at, created_at
			  FROM legal_hold_custodians
			 WHERE tenant_id = $1 AND hold_id = $2
			 ORDER BY created_at ASC
		`, tenantID, holdID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c Custodian
			if err := rows.Scan(&c.ID, &c.HoldID, &c.UserID, &c.NotifiedAt, &c.AcknowledgedAt, &c.CreatedAt); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// ListEvents wraps the package-level ListEvents so handlers can hit
// the chain log without holding their own pool reference.
func (s *HoldsService) ListEvents(ctx context.Context, tenantID, holdID uuid.UUID) ([]HoldEvent, error) {
	return ListEvents(ctx, s.pool, tenantID, holdID)
}

// VerifyChain wraps the package-level VerifyChain.
func (s *HoldsService) VerifyChain(ctx context.Context, tenantID, holdID uuid.UUID) (VerifyChainResult, error) {
	return VerifyChain(ctx, s.pool, tenantID, holdID)
}

// MyHoldRow is the wire shape for the custodian inbox at
// /legal-holds/my. Bundles a small slice of hold metadata so the UI
// doesn't need a second roundtrip per row.
type MyHoldRow struct {
	HoldID          uuid.UUID  `json:"hold_id"`
	HoldName        string     `json:"hold_name"`
	MatterReference string     `json:"matter_reference,omitempty"`
	IsActive        bool       `json:"is_active"`
	NotifiedAt      *time.Time `json:"notified_at,omitempty"`
	AcknowledgedAt  *time.Time `json:"acknowledged_at,omitempty"`
}

// ListMyHolds returns every hold the user is a custodian on. Active
// + released both surface so the user sees "you were a custodian on
// matter X (released)".
func (s *HoldsService) ListMyHolds(ctx context.Context, tenantID, userID uuid.UUID) ([]MyHoldRow, error) {
	var out []MyHoldRow
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT h.id, h.name, COALESCE(h.matter_reference, ''),
			       h.is_active, c.notified_at, c.acknowledged_at
			  FROM legal_hold_custodians c
			  JOIN legal_holds h
			    ON h.tenant_id = c.tenant_id AND h.id = c.hold_id
			 WHERE c.tenant_id = $1 AND c.user_id = $2
			 ORDER BY h.is_active DESC, c.created_at DESC
		`, tenantID, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r MyHoldRow
			if err := rows.Scan(&r.HoldID, &r.HoldName, &r.MatterReference,
				&r.IsActive, &r.NotifiedAt, &r.AcknowledgedAt); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}
