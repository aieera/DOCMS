package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/services/audit/internal/model"
)

// ErrDifferingAfterCutover refuses a re-baseline: an event written at or
// after the canonical-hash cutover does not recompute, so the difference
// is not the pre-fix hashing — it is tampering or a bug. Investigate; do
// not re-baseline.
var ErrDifferingAfterCutover = errors.New("differing event at or after the cutover: investigate, do not re-baseline")

// DifferingEvent identifies one event whose stored link/hash differs.
type DifferingEvent struct {
	ID        string
	CreatedAt time.Time
}

// RebaselineResult reports one tenant's re-baseline.
type RebaselineResult struct {
	Events            int             // events walked
	Rewritten         int             // events whose stored link or hash differs from the canonical form
	First, Last       *DifferingEvent // first and last differing event, chain order
	AtOrAfterCutover  int             // differing events with created_at >= cutover (cutover given)
	OldHead, NewHead  string          // stored head before the run; canonical head (before any marker)
	Applied           bool            // rows were rewritten and the marker appended
	CutoverConsidered bool
}

// RebaselineChain is the one-time cutover for chains written before the
// canonical-hash fix (07f13d71), whose v2 hashes covered the raw envelope
// bytes and therefore never verify. Under the tenant's chain lock it walks
// the chain in order and recomputes every link with expected (the
// verifier's own algorithm: legacy stays legacy, v2 over the stored
// values), recording which events differ.
//
// cutover, when non-nil, is when the canonical-hash fix was deployed: a
// differing event created at or after it cannot be pre-fix hashing.
// apply requires a cutover; if any differing event is at or after it the
// tenant is refused with ErrDifferingAfterCutover and nothing is written.
// Otherwise every differing row is rewritten in place and
// marker(old, new, n) is appended through the normal chained append, all
// in one transaction. With apply=false nothing is written.
//
// This is the ONLY writer of non-canonical→canonical hashes. It UPDATEs
// audit_events directly, which only the table owner / a BYPASSRLS
// operator role can do — dms_app's UPDATE is revoked (WORM).
func (r *Repository) RebaselineChain(ctx context.Context, tenantID string, apply bool, cutover *time.Time,
	expected func(prev string, e *model.AuditEvent) string,
	hash func(prev string, e *model.AuditEvent) string,
	marker func(oldHead, newHead string, rewritten int) *model.AuditEvent,
) (RebaselineResult, error) {
	var res RebaselineResult
	if apply && cutover == nil {
		return res, errors.New("rebaseline: apply requires a cutover")
	}
	res.CutoverConsidered = cutover != nil
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

		type rewrite struct{ id, prev, hash string }
		var rewrites []rewrite
		prev := ""
		for _, e := range events {
			want := expected(prev, e)
			if want != e.EventHash || prev != e.PreviousHash {
				res.Rewritten++
				d := &DifferingEvent{ID: e.ID, CreatedAt: e.CreatedAt.UTC()}
				if res.First == nil {
					res.First = d
				}
				res.Last = d
				if cutover != nil && !e.CreatedAt.Before(*cutover) {
					res.AtOrAfterCutover++
				}
				rewrites = append(rewrites, rewrite{e.ID, prev, want})
			}
			res.OldHead = e.EventHash
			prev = want
		}
		res.NewHead = prev
		if !apply || res.Rewritten == 0 {
			return nil
		}
		if res.AtOrAfterCutover > 0 {
			return fmt.Errorf("%w (%d event(s), first differing %s at %s)", ErrDifferingAfterCutover,
				res.AtOrAfterCutover, res.First.ID, res.First.CreatedAt.Format(time.RFC3339Nano))
		}
		for _, w := range rewrites {
			if _, err := tx.Exec(ctx,
				`UPDATE audit_events SET previous_hash = $3, event_hash = $4 WHERE tenant_id = $1 AND id = $2`,
				tenantID, w.id, w.prev, w.hash); err != nil {
				return fmt.Errorf("rebaseline event %s: %w", w.id, err)
			}
		}
		if err := appendInTx(ctx, tx, marker(res.OldHead, res.NewHead, res.Rewritten), hash); err != nil {
			return fmt.Errorf("append rebaseline marker: %w", err)
		}
		res.Applied = true
		return nil
	})
	return res, err
}
