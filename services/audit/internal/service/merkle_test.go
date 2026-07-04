package service

import (
	"testing"
	"time"

	"github.com/aieera/sedoc/services/audit/internal/model"
)

// chainedEvents builds a small, internally-consistent hash chain the way the
// ingest path does: each event's hash = computeHash(prevHash, fields).
func chainedEvents(tenant, resourceID string, n int) []*model.AuditEvent {
	out := make([]*model.AuditEvent, 0, n)
	prev := ""
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		e := &model.AuditEvent{
			ID:           string(rune('a'+i)) + "-id",
			TenantID:     tenant,
			Actor:        "user-1",
			Action:       "document.read",
			ResourceID:   resourceID,
			ResourceType: "document",
			CreatedAt:    base.Add(time.Duration(i) * time.Minute),
		}
		e.PreviousHash = prev
		e.EventHash = computeHash(prev, e.TenantID, e.Actor, e.Action, e.ResourceID, e.CreatedAt)
		prev = e.EventHash
		out = append(out, e)
	}
	return out
}

func TestBuildMerkleProof_ValidChain(t *testing.T) {
	events := chainedEvents("t1", "doc-1", 5)
	p := buildMerkleProof("t1", "document", "doc-1", events)

	if !p.Valid {
		t.Fatalf("expected valid proof, got broken_at=%s", p.BrokenAt)
	}
	if p.Count != 5 {
		t.Fatalf("expected count 5, got %d", p.Count)
	}
	if p.RootHash == "" {
		t.Fatal("expected a non-empty Merkle root")
	}
	if len(p.LeafHashes) != 5 {
		t.Fatalf("expected 5 leaves, got %d", len(p.LeafHashes))
	}
}

func TestBuildMerkleProof_DeterministicRoot(t *testing.T) {
	events := chainedEvents("t1", "doc-1", 4)
	a := buildMerkleProof("t1", "document", "doc-1", events)
	b := buildMerkleProof("t1", "document", "doc-1", events)
	if a.RootHash != b.RootHash {
		t.Fatalf("root not deterministic: %s vs %s", a.RootHash, b.RootHash)
	}
}

// The DoD: verify-integrity detects a tampered event in a test.
//
// Case 1 — content edited, stored hash left stale (a raw DB UPDATE): the
// per-event hash recompute catches it (Valid=false, BrokenAt set).
func TestBuildMerkleProof_DetectsContentTamper(t *testing.T) {
	events := chainedEvents("t1", "doc-1", 5)
	events[2].Action = "document.delete" // tamper content, leave EventHash stale
	tampered := buildMerkleProof("t1", "document", "doc-1", events)

	if tampered.Valid {
		t.Fatal("expected proof to be invalid after content tamper")
	}
	if tampered.BrokenAt != events[2].ID {
		t.Fatalf("expected broken_at=%s, got %s", events[2].ID, tampered.BrokenAt)
	}
}

// Case 2 — attacker edits content AND recomputes that event's hash so the leaf
// is self-consistent. The per-event check passes, but the Merkle ROOT changes
// vs the trusted (e.g. checkpointed) root — so the proof still detects it.
func TestBuildMerkleProof_RootChangesOnRehash(t *testing.T) {
	events := chainedEvents("t1", "doc-1", 5)
	good := buildMerkleProof("t1", "document", "doc-1", events)

	events[2].Action = "document.delete"
	events[2].EventHash = computeHash(events[2].PreviousHash, events[2].TenantID,
		events[2].Actor, events[2].Action, events[2].ResourceID, events[2].CreatedAt)
	rehashed := buildMerkleProof("t1", "document", "doc-1", events)

	if rehashed.RootHash == good.RootHash {
		t.Fatal("expected the Merkle root to change after the leaf was rehashed")
	}
}

func TestMerkleRoot_EmptyAndSingle(t *testing.T) {
	if r := merkleRoot(nil); r != "" {
		t.Fatalf("expected empty root for no leaves, got %q", r)
	}
	single := merkleRoot([]string{"aa"})
	if single == "" {
		t.Fatal("expected a root for a single leaf")
	}
}
