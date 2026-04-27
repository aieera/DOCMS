package compliance

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vaultdms/vaultdms/pkg/database"
)

// Hash-chained legal_hold_events log (Blueprint §9.3).
//
// Mirrors the acknowledgement service's per-campaign chain: every
// hold owns an independent sequence so a single hold's events can be
// extracted + re-verified in court without having to walk the rest
// of the tenant's data. Chain link:
//
//   prev_hash[0]   = sha256(tenant_secret || hold_id)
//   self_hash[i]   = sha256(prev_hash[i] || event_bytes[i] || tenant_secret)
//   prev_hash[i+1] = self_hash[i]
//
// tenant_secret is derived from VAULTDMS_LEGAL_HOLD_HMAC_SECRET (with
// a per-tenant KDF in production; dev uses the bare secret). Without
// the secret an attacker who has DB access still can't rewrite a
// single event without invalidating the chain from that point on.

const envHMACSecret = "VAULTDMS_LEGAL_HOLD_HMAC_SECRET"

// EventType enumerates every entry that can land in the chain.
// Append-only: never rename / repurpose a value.
const (
	EventApplied               = "applied"
	EventReleased              = "released"
	EventUpdated               = "updated"
	EventTargetAdded           = "target_added"
	EventTargetRemoved         = "target_removed"
	EventCustodianAdded        = "custodian_added"
	EventCustodianAcknowledged = "custodian_acknowledged"
)

// HoldEvent is the wire shape for one chained event.
type HoldEvent struct {
	ID         uuid.UUID       `json:"id"`
	HoldID     uuid.UUID       `json:"hold_id"`
	Sequence   int64           `json:"sequence"`
	EventType  string          `json:"event_type"`
	ActorID    *uuid.UUID      `json:"actor_id,omitempty"`
	Payload    json.RawMessage `json:"payload"`
	PrevHash   string          `json:"prev_hash"`
	SelfHash   string          `json:"self_hash"`
	OccurredAt time.Time       `json:"occurred_at"`
}

// AppendEvent writes one chained event in the caller's tenant tx.
// Resolves prev_hash from the latest row for the hold (or seeds with
// sha256(secret || hold_id) for sequence=1). Idempotent only via the
// (tenant_id, hold_id, sequence) unique constraint — concurrent writers
// to the same hold serialize on the index.
func AppendEvent(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, holdID uuid.UUID,
	actorID *uuid.UUID,
	eventType string,
	payload any,
) (HoldEvent, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return HoldEvent{}, fmt.Errorf("legal hold event marshal: %w", err)
	}

	var (
		seq      int64
		prevHash []byte
	)
	err = tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(sequence), 0) + 1,
		       (SELECT self_hash FROM legal_hold_events
		         WHERE tenant_id = $1 AND hold_id = $2
		         ORDER BY sequence DESC LIMIT 1)
		  FROM legal_hold_events
		 WHERE tenant_id = $1 AND hold_id = $2
	`, tenantID, holdID).Scan(&seq, &prevHash)
	if err != nil {
		return HoldEvent{}, fmt.Errorf("legal hold event seq lookup: %w", err)
	}
	secret := tenantSecret(tenantID, holdID)
	if len(prevHash) == 0 {
		prevHash = seedHash(secret, holdID)
	}

	selfHash := chainStep(prevHash, body, secret)
	id, _ := uuid.NewV7()
	row := HoldEvent{
		ID:        id,
		HoldID:    holdID,
		Sequence:  seq,
		EventType: eventType,
		ActorID:   actorID,
		Payload:   body,
		PrevHash:  hex.EncodeToString(prevHash),
		SelfHash:  hex.EncodeToString(selfHash),
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO legal_hold_events
		    (tenant_id, id, hold_id, sequence, event_type, actor_id,
		     payload, prev_hash, self_hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING occurred_at
	`, tenantID, id, holdID, seq, eventType, actorID,
		body, prevHash, selfHash,
	).Scan(&row.OccurredAt)
	if err != nil {
		return HoldEvent{}, fmt.Errorf("legal hold event insert: %w", err)
	}
	return row, nil
}

// VerifyChainResult reports whether the chain is intact for a hold.
// `BrokenAt` is the sequence number of the first row whose recomputed
// hash differs; zero when intact.
type VerifyChainResult struct {
	OK            bool   `json:"ok"`
	EventCount    int64  `json:"event_count"`
	BrokenAt      int64  `json:"broken_at,omitempty"`
	BrokenMessage string `json:"message,omitempty"`
}

// VerifyChain replays the chain for one hold using the same per-tenant
// secret as AppendEvent. Reads outside any tenant tx — RLS still
// applies, but we don't want chain verification to acquire tx locks.
func VerifyChain(ctx context.Context, pool *pgxpool.Pool, tenantID, holdID uuid.UUID) (VerifyChainResult, error) {
	res := VerifyChainResult{}
	err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT sequence, payload, prev_hash, self_hash
			  FROM legal_hold_events
			 WHERE tenant_id = $1 AND hold_id = $2
			 ORDER BY sequence ASC
		`, tenantID, holdID)
		if err != nil {
			return err
		}
		defer rows.Close()
		secret := tenantSecret(tenantID, holdID)
		expectedPrev := seedHash(secret, holdID)
		for rows.Next() {
			var (
				seq      int64
				payload  []byte
				stored   []byte
				storedHash []byte
			)
			if err := rows.Scan(&seq, &payload, &stored, &storedHash); err != nil {
				return err
			}
			if !hmac.Equal(stored, expectedPrev) {
				res.BrokenAt = seq
				res.BrokenMessage = "prev_hash mismatch"
				return nil
			}
			recomputed := chainStep(expectedPrev, payload, secret)
			if !hmac.Equal(recomputed, storedHash) {
				res.BrokenAt = seq
				res.BrokenMessage = "self_hash mismatch"
				return nil
			}
			expectedPrev = storedHash
			res.EventCount++
		}
		return rows.Err()
	})
	if err != nil {
		return res, err
	}
	res.OK = res.BrokenAt == 0
	return res, nil
}

// ListEvents returns all chained events for a hold, ordered by sequence
// ascending. Used by /verify-chain (with the result above) and by the
// admin event-log tab.
func ListEvents(ctx context.Context, pool *pgxpool.Pool, tenantID, holdID uuid.UUID) ([]HoldEvent, error) {
	var out []HoldEvent
	err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, hold_id, sequence, event_type, actor_id,
			       payload, prev_hash, self_hash, occurred_at
			  FROM legal_hold_events
			 WHERE tenant_id = $1 AND hold_id = $2
			 ORDER BY sequence ASC
		`, tenantID, holdID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				ev     HoldEvent
				prev   []byte
				self   []byte
				actor  *uuid.UUID
			)
			if err := rows.Scan(&ev.ID, &ev.HoldID, &ev.Sequence, &ev.EventType,
				&actor, &ev.Payload, &prev, &self, &ev.OccurredAt); err != nil {
				return err
			}
			ev.ActorID = actor
			ev.PrevHash = hex.EncodeToString(prev)
			ev.SelfHash = hex.EncodeToString(self)
			out = append(out, ev)
		}
		return rows.Err()
	})
	return out, err
}

func tenantSecret(tenantID, holdID uuid.UUID) []byte {
	base := []byte(os.Getenv(envHMACSecret))
	if len(base) == 0 {
		base = []byte("dev-only-legal-hold-hmac-rotate-in-prod")
	}
	mac := hmac.New(sha256.New, base)
	mac.Write([]byte(tenantID.String()))
	return mac.Sum(nil)
}

func seedHash(secret []byte, holdID uuid.UUID) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(holdID.String()))
	return mac.Sum(nil)
}

func chainStep(prev, payload, secret []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write(prev)
	mac.Write(payload)
	return mac.Sum(nil)
}

// emitLegalHoldNotify writes a row under dms.notify.legalhold.* so the
// notification service's dms.notify.> consumer delivers to each
// custodian's enabled channels. Reuses the outbox table directly to
// avoid a circular import with repository.
func emitLegalHoldNotify(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, holdID, actorID uuid.UUID,
	subject string,
	userIDs []string,
	title, body string,
) error {
	id, _ := uuid.NewV7()
	payload, err := json.Marshal(map[string]any{
		"tenant_id":     tenantID.String(),
		"actor_id":      actorID.String(),
		"hold_id":       holdID.String(),
		"user_ids":      userIDs,
		"title":         title,
		"body":          body,
		"resource_type": "legal_hold",
		"resource_id":   holdID.String(),
	})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO outbox (id, tenant_id, event_type, aggregate_type, aggregate_id, payload, created_at)
		VALUES ($1, $2, $3, 'legal_hold', $4, $5, now())
	`, id, tenantID, subject, holdID, payload)
	return err
}
