// ADR 0064 — workflow_delegations CRUD.
//
// Tenant-wide forwarding rules with time bounds. The service layer
// rejects cycles (a → b → a) by walking before insert; this repo
// just persists.
package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// DelegationRow mirrors the workflow_delegations row.
type DelegationRow struct {
	ID          string     `json:"id"`
	TenantID    string     `json:"tenant_id"`
	DelegatorID string     `json:"delegator_id"`
	DelegateID  string     `json:"delegate_id"`
	StartsAt    time.Time  `json:"starts_at"`
	EndsAt      time.Time  `json:"ends_at"`
	Reason      string     `json:"reason,omitempty"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

// CreateDelegation inserts a forward rule. Caller has already
// checked for cycles + authority (only the delegator or a tenant
// admin can create one of these).
func (r *Repository) CreateDelegation(ctx context.Context, d *DelegationRow) error {
	if d.ID == "" {
		d.ID = newID()
	}
	return r.runTenant(ctx, d.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO workflow_delegations (tenant_id, id, delegator_id, delegate_id, starts_at, ends_at, reason)
			VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''))`,
			d.TenantID, d.ID, d.DelegatorID, d.DelegateID, d.StartsAt, d.EndsAt, d.Reason)
		return err
	})
}

// RevokeDelegation soft-deletes a forward rule (sets revoked_at).
// We keep the row so the audit chain remains queryable.
func (r *Repository) RevokeDelegation(ctx context.Context, tenantID, id string) error {
	return r.runTenant(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE workflow_delegations
			    SET revoked_at = now()
			  WHERE tenant_id = $1 AND id = $2 AND revoked_at IS NULL`,
			tenantID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errors.New("delegation not found or already revoked")
		}
		return nil
	})
}

// ListDelegationsForUser returns every active or future-active
// forward rule where the user is the delegator. The settings page
// shows these so the user can revoke if their plans change.
func (r *Repository) ListDelegationsForUser(ctx context.Context, tenantID, userID string) ([]DelegationRow, error) {
	var out []DelegationRow
	err := r.runTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, tenant_id::text, delegator_id::text, delegate_id::text,
			       starts_at, ends_at, COALESCE(reason, ''), revoked_at, created_at
			  FROM workflow_delegations
			 WHERE tenant_id    = $1
			   AND delegator_id = $2
			   AND ends_at      > now()
			 ORDER BY starts_at DESC`,
			tenantID, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d DelegationRow
			if err := rows.Scan(&d.ID, &d.TenantID, &d.DelegatorID, &d.DelegateID,
				&d.StartsAt, &d.EndsAt, &d.Reason, &d.RevokedAt, &d.CreatedAt); err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

// HasDelegationCycle walks forward rules from `delegator → delegate`
// looking for a path back to the original delegator. Used pre-insert
// to reject `a → b` when `b → a` already exists.
//
// We walk shallow because chains rarely exceed 2 levels in practice;
// deep cycles fall out of the same algorithm.
func (r *Repository) HasDelegationCycle(ctx context.Context, tenantID, delegator, delegate string) (bool, error) {
	if delegator == delegate {
		return true, nil
	}
	visited := map[string]bool{delegator: true}
	current := delegate
	const maxDepth = 8
	var found bool
	err := r.runTenant(ctx, tenantID, func(tx pgx.Tx) error {
		for depth := 0; depth < maxDepth; depth++ {
			if visited[current] {
				found = true
				return nil
			}
			visited[current] = true
			var next string
			err := tx.QueryRow(ctx, `
				SELECT delegate_id::text FROM workflow_delegations
				 WHERE tenant_id = $1
				   AND delegator_id = $2
				   AND revoked_at IS NULL
				   AND ends_at > now()
				 ORDER BY created_at DESC LIMIT 1`,
				tenantID, current,
			).Scan(&next)
			if err != nil {
				if err == pgx.ErrNoRows {
					return nil
				}
				return err
			}
			current = next
		}
		return nil
	})
	return found, err
}

var _ = uuid.Nil // keep uuid import explicit for future helpers
