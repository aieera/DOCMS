package service

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"github.com/aieera/sedoc/services/audit/internal/model"
)

// merkleRoot computes a binary SHA-256 Merkle root over the ordered leaf
// hashes. Leaves are the hex event hashes; an odd node at any level is paired
// with itself (the standard duplicate-last rule). Empty input -> "".
func merkleRoot(leavesHex []string) string {
	if len(leavesHex) == 0 {
		return ""
	}
	// Decode leaves to raw bytes; a non-hex leaf falls back to hashing its
	// bytes so a malformed value can't panic the proof.
	level := make([][]byte, 0, len(leavesHex))
	for _, h := range leavesHex {
		b, err := hex.DecodeString(h)
		if err != nil {
			sum := sha256.Sum256([]byte(h))
			b = sum[:]
		}
		level = append(level, b)
	}
	for len(level) > 1 {
		next := make([][]byte, 0, (len(level)+1)/2)
		for i := 0; i < len(level); i += 2 {
			left := level[i]
			right := left // duplicate-last for an odd node
			if i+1 < len(level) {
				right = level[i+1]
			}
			h := sha256.New()
			h.Write(left)
			h.Write(right)
			next = append(next, h.Sum(nil))
		}
		level = next
	}
	return hex.EncodeToString(level[0])
}

// buildMerkleProof recomputes each event's hash from its stored fields, flags
// the first mismatch (tampered content), and builds the Merkle root over the
// stored event hashes. Pure + deterministic (events are sorted by created_at
// then id) so it is unit-testable without a DB.
func buildMerkleProof(tenantID, resourceType, resourceID string, events []*model.AuditEvent) *model.MerkleProof {
	ev := make([]*model.AuditEvent, len(events))
	copy(ev, events)
	sort.SliceStable(ev, func(i, j int) bool {
		if ev[i].CreatedAt.Equal(ev[j].CreatedAt) {
			return ev[i].ID < ev[j].ID
		}
		return ev[i].CreatedAt.Before(ev[j].CreatedAt)
	})

	proof := &model.MerkleProof{
		TenantID: tenantID, ResourceType: resourceType, ResourceID: resourceID,
		Count: len(ev), Valid: true,
	}
	leaves := make([]string, 0, len(ev))
	for _, e := range ev {
		// Version-aware: v2 events must be recomputed over all fields, legacy
		// events over the old set. Using the fixed legacy algo here would both
		// falsely break every v2 event AND leave the same fields (details/ip/
		// user_agent/…) unverified in the per-resource proof.
		expected := expectedHashFor(e.PreviousHash, e)
		if proof.Valid && e.EventHash != expected {
			proof.Valid = false
			proof.BrokenAt = e.ID
			proof.BrokenHash = e.EventHash
			proof.ExpectedHash = expected
		}
		leaves = append(leaves, e.EventHash)
	}
	proof.LeafHashes = leaves
	proof.RootHash = merkleRoot(leaves)
	if len(ev) > 0 {
		first, last := ev[0], ev[len(ev)-1]
		proof.FirstEventID, proof.LastEventID = first.ID, last.ID
		fa, la := first.CreatedAt, last.CreatedAt
		proof.FirstAt, proof.LastAt = &fa, &la
	}
	return proof
}
