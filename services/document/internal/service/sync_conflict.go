package service

import "github.com/google/uuid"

// StaleBaseVersion reports whether an optimistic-concurrency base version no
// longer matches the document's current head — the signal a sync client turns
// into a conflict (§sync). A nil base skips the check (last-write-wins); a base
// equal to the current head (or both nil for a first version) is fresh.
func StaleBaseVersion(base, current *uuid.UUID) bool {
	if base == nil {
		return false
	}
	cur := uuid.Nil
	if current != nil {
		cur = *current
	}
	return *base != cur
}
