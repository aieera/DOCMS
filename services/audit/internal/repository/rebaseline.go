package repository

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/services/audit/internal/model"
)

// RebaselineResult reports one tenant's re-baseline.
type RebaselineResult struct {
	Events    int    // events walked
	Rewritten int    // events whose stored link or hash differed from the canonical form
	OldHead   string // stored head hash before the run
	NewHead   string // canonical head hash (before any marker)
	Applied   bool   // rows were rewritten and the marker appended
}

// RebaselineChain is the one-time cutover for chains written before the
// canonical-hash fix (07f13d71), whose v2 hashes covered the raw envelope
// bytes and therefore never verify. Under the tenant's chain lock it walks
// the chain in order and recomputes every link with expected (the
// verifier's own algorithm: legacy stays legacy, v2 over the stored
// values). With apply=false nothing is written. With apply=true every
// differing row is rewritten in place and marker(old, new, n) is appended
// through the normal chained append, all in one transaction.
//
// This is the ONLY writer of non-canonical→canonical hashes. It UPDATEs
// audit_events directly, which only the table owner / a BYPASSRLS
// operator role can do — dms_app's UPDATE is revoked (WORM).
func (r *Repository) RebaselineChain(ctx context.Context, tenantID string, apply bool,
	expected func(prev string, e *model.AuditEvent) string,
	hash func(prev string, e *model.AuditEvent) string,
	marker func(oldHead, newHead string, rewritten int) *model.AuditEvent,
) (RebaselineResult, error) {
	var res RebaselineResult
	err := r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		if err := lockChain(ctx, tx, tenantID); err != nil {
			return err
		}
		events, err := scanEvents(tx.Query(ctx,
			selectEvents+` WHERE tenant_id = $1 ORDER BY created_at ASC, id ASC`, tenantID))
		if err != nil {
			return fmt.Errorf("rebaseline load: %w", err)
		}
		res.Events = len(events)
		prev := ""
		for _, e := range events {
			want := expected(prev, e)
			if want != e.EventHash || prev != e.PreviousHash {
				res.Rewritten++
				if apply {
					if _, err := tx.Exec(ctx,
						`UPDATE audit_events SET previous_hash = $3, event_hash = $4 WHERE tenant_id = $1 AND id = $2`,
						tenantID, e.ID, prev, want); err != nil {
						return fmt.Errorf("rebaseline event %s: %w", e.ID, err)
					}
				}
			}
			res.OldHead = e.EventHash
			prev = want
		}
		res.NewHead = prev
		if !apply || res.Rewritten == 0 {
			return nil
		}
		if err := appendInTx(ctx, tx, marker(res.OldHead, res.NewHead, res.Rewritten), hash); err != nil {
			return fmt.Errorf("append rebaseline marker: %w", err)
		}
		res.Applied = true
		return nil
	})
	return res, err
}
