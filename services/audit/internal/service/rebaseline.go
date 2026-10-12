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
// checkpoint does not vouch for and no re-baseline marker explains.
var ErrUncertifiedHead = errors.New("chain does not extend the latest signed checkpoint: investigate, do not re-baseline")

// RebaselineResult is one tenant's re-baseline outcome.
type RebaselineResult struct {
	repository.RebaselineResult
	TenantID     string
	Checkpointed bool // a signed checkpoint now anchors the new head
}

// Rebaseline re-hashes a tenant's chain into the canonical form (see
// repository.RebaselineChain). apply=false is a dry run. With apply=true
// and at least one differing event, the rows are rewritten, an
// audit.chain_rebaselined marker naming the operator, both head hashes and
// the cutover is appended, and a fresh signed checkpoint anchors the new head. apply
// without a signing key returns ErrSigningDisabled before anything is
// written. A chain that already verifies is left untouched — except that
// with apply, a head the latest checkpoint does not cover is signed (a
// checkpoint that failed after an earlier run committed). A second run
// after a complete one writes nothing.
func (s *Service) Rebaseline(ctx context.Context, tenantID string, apply bool, operator string, cutover *time.Time) (RebaselineResult, error) {
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
	unlock := s.lockTenant(tenantID)
	res, err := s.repo.RebaselineChain(ctx, tenantID, apply, cutover, expectedHashFor, computeEventHash, marker)
	unlock()
	out.RebaselineResult = res // also on refusal, so the caller can show the differing range
	if err != nil {
		return out, fmt.Errorf("rebaseline %s: %w", tenantID, err)
	}
	if !res.Applied {
		if !apply || res.Rewritten > 0 {
			return out, nil
		}
		// Nothing to rewrite: the chain verifies internally. If an earlier
		// run's checkpoint never landed (it is written after the rewrite
		// commits), the latest checkpoint does not cover the head — sign
		// it now, but only if the signed history vouches for this chain.
		sign, err := s.mayResign(ctx, tenantID)
		if err != nil {
			return out, fmt.Errorf("rebaseline %s: %w", tenantID, err)
		}
		if !sign {
			return out, nil
		}
	}
	if _, err := s.CreateCheckpoint(ctx, tenantID); err != nil {
		return out, fmt.Errorf("rebaseline %s: checkpoint: %w", tenantID, err)
	}
	out.Checkpointed = true
	return out, nil
}

// mayResign decides whether an --apply run that rewrote nothing may sign
// the current head. It returns (false, nil) when the latest checkpoint
// already covers the head (nothing to do) and ErrUncertifiedHead when
// signing would certify a chain the signed history does not vouch for.
//
// An internally consistent chain is not proof of authenticity: someone
// with DB write access but no key can rewrite every hash consistently.
// So, with a latest checkpoint present, its signature must verify and
// either the chain must extend it (the event at cp.EventCount-1 still has
// the signed head hash) or — the repair case — an audit.chain_rebaselined
// marker must sit at or after position cp.EventCount, i.e. a re-baseline
// explains the rewritten prefix and no checkpoint was signed after it.
// With no checkpoint at all, the first one is signed (the caller only gets
// here when the chain verifies).
func (s *Service) mayResign(ctx context.Context, tenantID string) (bool, error) {
	events, err := s.repo.ListAll(ctx, tenantID)
	if err != nil {
		return false, fmt.Errorf("checkpoint check: %w", err)
	}
	if len(events) == 0 {
		return false, nil
	}
	cp, err := s.repo.LatestCheckpoint(ctx, tenantID)
	if err != nil {
		return false, fmt.Errorf("checkpoint check: %w", err)
	}
	if cp == nil {
		return true, nil
	}
	head := events[len(events)-1].EventHash
	if cp.HeadHash == head && cp.EventCount == int64(len(events)) {
		return false, nil // already covered
	}
	sig, derr := base64.StdEncoding.DecodeString(cp.Signature)
	if derr != nil || cp.KeyID != s.keyID ||
		!ed25519.Verify(s.signerPub, CanonicalCheckpointMessage(cp.TenantID, cp.HeadHash, cp.EventCount), sig) {
		return false, fmt.Errorf("%w: latest checkpoint signature does not verify with this key", ErrUncertifiedHead)
	}
	if cp.EventCount >= 1 && cp.EventCount <= int64(len(events)) && events[cp.EventCount-1].EventHash == cp.HeadHash {
		return true, nil // the chain extends the signed prefix
	}
	for i := cp.EventCount; i < int64(len(events)); i++ {
		if i >= 0 && events[i].Action == "audit.chain_rebaselined" {
			return true, nil // a re-baseline after the checkpoint explains the change
		}
	}
	return false, fmt.Errorf("%w: the signed prefix (%d events) no longer matches and no re-baseline marker follows it",
		ErrUncertifiedHead, cp.EventCount)
}
