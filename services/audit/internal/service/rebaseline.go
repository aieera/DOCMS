package service

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aieera/sedoc/services/audit/internal/model"
	"github.com/aieera/sedoc/services/audit/internal/repository"
)

// ErrUncertifiedHead refuses to sign a chain head that the latest signed
// checkpoint does not vouch for (or, without --allow-first-checkpoint, a
// tenant that has no checkpoint at all).
var ErrUncertifiedHead = errors.New("chain does not extend the latest signed checkpoint: investigate, do not re-baseline")

// RebaselineResult is one tenant's re-baseline outcome.
type RebaselineResult struct {
	repository.RebaselineResult
	TenantID     string
	Checkpointed bool // a signed checkpoint now anchors the new head
	NoCheckpoint bool // refused: the tenant has no checkpoint and the first one was not opted into
}

// RebaselineOptions controls one tenant's re-baseline.
type RebaselineOptions struct {
	Apply                bool       // write (default: dry run)
	Operator             string     // recorded on the marker event
	Cutover              *time.Time // deploy time of the canonical-hash fix (required with Apply)
	AllowFirstCheckpoint bool       // sign a tenant that has no checkpoint at all (incident-level decision)
}

// Rebaseline re-hashes a tenant's chain into the canonical form (see
// repository.RebaselineChain). Apply=false is a dry run. With Apply and at
// least one differing event, one transaction rewrites the rows, appends an
// audit.chain_rebaselined marker (operator, both head hashes, cutover;
// informational) and inserts a checkpoint signed over the new head: all of
// it commits or none does. Apply without a signing key returns
// ErrSigningDisabled before anything is written.
//
// A chain that already verifies is never rewritten. With Apply, its head
// is signed only if the latest checkpoint vouches for it (see mayResign).
// A second run after a complete one writes nothing.
func (s *Service) Rebaseline(ctx context.Context, tenantID string, opt RebaselineOptions) (RebaselineResult, error) {
	apply, operator, cutover := opt.Apply, opt.Operator, opt.Cutover
	out := RebaselineResult{TenantID: tenantID}
	if apply && s.signer == nil {
		// Never rewrite a chain that cannot be re-anchored by a signed
		// checkpoint (an uncertified tail is what H-12 exists to prevent).
		return out, ErrSigningDisabled
	}
	marker := func(oldHead, newHead string, rewritten int) *model.AuditEvent {
		details, _ := json.Marshal(map[string]any{
			"old_head_hash":   oldHead,
			"new_head_hash":   newHead,
			"rewritten_count": rewritten,
			"operator":        operator,
			"cutover":         cutover.UTC().Format(time.RFC3339),
			"reason":          "one-time cutover to canonical event hashes",
		})
		return &model.AuditEvent{
			ID:        newID(),
			TenantID:  tenantID,
			ActorName: operator,
			Action:    "audit.chain_rebaselined",
			Details:   details,
			CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
		}
	}
	var sign func(head string, count int64) *model.AuditCheckpoint
	if s.signer != nil {
		sign = func(head string, count int64) *model.AuditCheckpoint { return s.signCheckpoint(tenantID, head, count) }
	}
	unlock := s.lockTenant(tenantID)
	res, err := s.repo.RebaselineChain(ctx, tenantID, apply, cutover, expectedHashFor, computeEventHash, marker, sign)
	unlock()
	out.RebaselineResult = res // also on refusal, so the caller can show the differing range
	if err != nil {
		return out, fmt.Errorf("rebaseline %s: %w", tenantID, err)
	}
	if res.Applied {
		out.Checkpointed = true // committed with the rewrite
		return out, nil
	}
	if !apply || res.Rewritten > 0 {
		return out, nil
	}
	// Nothing to rewrite: the chain verifies internally. Sign its head
	// only if the signed history vouches for it.
	resign, noCheckpoint, err := s.mayResign(ctx, tenantID, opt.AllowFirstCheckpoint)
	out.NoCheckpoint = noCheckpoint
	if err != nil {
		return out, fmt.Errorf("rebaseline %s: %w", tenantID, err)
	}
	if !resign {
		return out, nil
	}
	if _, err := s.CreateCheckpoint(ctx, tenantID); err != nil {
		return out, fmt.Errorf("rebaseline %s: checkpoint: %w", tenantID, err)
	}
	out.Checkpointed = true
	return out, nil
}

// mayResign decides whether an --apply run that rewrote nothing may sign
// the current head. It returns sign=false with no error when the latest
// checkpoint already covers the head (nothing to do), and
// ErrUncertifiedHead when signing would certify a chain the signed history
// does not vouch for.
//
// An internally consistent chain is not proof of authenticity: someone
// with DB write access but no key can rewrite every hash consistently, and
// can insert any row, an audit.chain_rebaselined marker included. So with
// a latest checkpoint present, its signature must verify with this key
// and the chain must still carry its signed prefix (the event at
// cp.EventCount-1 has the signed head hash): only appends since. There is
// no repair exception, because a re-baseline commits its checkpoint with
// the rewrite. With no checkpoint at all nothing can be cross-checked (a
// deleted-checkpoints incident looks the same), so the first one is signed
// only with allowFirst; otherwise the tenant is refused with
// noCheckpoint=true.
func (s *Service) mayResign(ctx context.Context, tenantID string, allowFirst bool) (sign, noCheckpoint bool, err error) {
	events, err := s.repo.ListAll(ctx, tenantID)
	if err != nil {
		return false, false, fmt.Errorf("checkpoint check: %w", err)
	}
	if len(events) == 0 {
		return false, false, nil
	}
	cp, err := s.repo.LatestCheckpoint(ctx, tenantID)
	if err != nil {
		return false, false, fmt.Errorf("checkpoint check: %w", err)
	}
	if cp == nil {
		if allowFirst {
			return true, true, nil
		}
		return false, true, fmt.Errorf("%w: the tenant has no checkpoint; signing its first one needs --allow-first-checkpoint (an incident-level decision)",
			ErrUncertifiedHead)
	}
	head := events[len(events)-1].EventHash
	if cp.HeadHash == head && cp.EventCount == int64(len(events)) {
		return false, false, nil // already covered
	}
	sig, derr := base64.StdEncoding.DecodeString(cp.Signature)
	if derr != nil || cp.KeyID != s.keyID ||
		!ed25519.Verify(s.signerPub, CanonicalCheckpointMessage(cp.TenantID, cp.HeadHash, cp.EventCount), sig) {
		return false, false, fmt.Errorf("%w: latest checkpoint signature does not verify with this key", ErrUncertifiedHead)
	}
	if cp.EventCount >= 1 && cp.EventCount <= int64(len(events)) && events[cp.EventCount-1].EventHash == cp.HeadHash {
		return true, false, nil // the chain extends the signed prefix
	}
	return false, false, fmt.Errorf("%w: the signed prefix (%d events) no longer matches the chain", ErrUncertifiedHead, cp.EventCount)
}
