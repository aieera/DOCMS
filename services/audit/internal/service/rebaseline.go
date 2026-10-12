package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/aieera/sedoc/services/audit/internal/model"
	"github.com/aieera/sedoc/services/audit/internal/repository"
)

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
// written. A chain that already verifies is left untouched, so a second
// run writes nothing.
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
		return out, nil
	}
	if _, err := s.CreateCheckpoint(ctx, tenantID); err != nil {
		return out, fmt.Errorf("rebaseline %s: checkpoint: %w", tenantID, err)
	}
	out.Checkpointed = true
	return out, nil
}
