package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"
)

// computeHash is the cornerstone of audit integrity. These tests pin the
// exact SHA-256 formula so future edits to the chaining function show up
// here as a failure instead of silently rewriting every audit row's
// signature.

func TestComputeHash_DeterministicForKnownInputs(t *testing.T) {
	ts, err := time.Parse(time.RFC3339Nano, "2026-04-17T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	got := computeHash("", "t1", "u1", "doc.created", "d1", ts)

	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%s|%s|%s|%s", "", "t1", "u1", "doc.created", "d1", ts.Format(time.RFC3339Nano))
	want := hex.EncodeToString(h.Sum(nil))

	if got != want {
		t.Fatalf("hash formula drifted:\n got  %s\n want %s", got, want)
	}
}

func TestComputeHash_ChangesWithPreviousHash(t *testing.T) {
	// Same tenant/actor/action/resource/time. Only `prev` differs. The
	// outputs MUST differ — that's what makes tamper detection work.
	ts := time.Unix(0, 0).UTC()
	a := computeHash("prev-A", "t1", "u1", "doc.created", "d1", ts)
	b := computeHash("prev-B", "t1", "u1", "doc.created", "d1", ts)
	if a == b {
		t.Fatal("hash must change when prev changes")
	}
}

func TestComputeHash_ChangesWithActor(t *testing.T) {
	ts := time.Unix(0, 0).UTC()
	a := computeHash("p", "t1", "actorA", "doc.created", "d1", ts)
	b := computeHash("p", "t1", "actorB", "doc.created", "d1", ts)
	if a == b {
		t.Fatal("hash must change when actor changes")
	}
}

func TestComputeHash_ChainsOverThreeEvents(t *testing.T) {
	// Simulate three sequential writes: each hash feeds the next.
	ts := time.Unix(0, 0).UTC()
	h1 := computeHash("", "t1", "u1", "a1", "r1", ts)
	h2 := computeHash(h1, "t1", "u1", "a2", "r2", ts.Add(time.Second))
	h3 := computeHash(h2, "t1", "u1", "a3", "r3", ts.Add(2*time.Second))

	// Verify: recomputing with the wrong previous breaks the chain.
	tampered := computeHash("wrong-prev", "t1", "u1", "a3", "r3", ts.Add(2*time.Second))
	if h3 == tampered {
		t.Fatal("tamper detection failed — hash collided with a different prev")
	}
	if h1 == h2 || h2 == h3 {
		t.Fatal("consecutive hashes should differ")
	}
}

func TestStrField_Robustness(t *testing.T) {
	m := map[string]any{"s": "hello", "n": 42, "b": true, "nil": nil}
	if got := strField(m, "s"); got != "hello" {
		t.Errorf("string: got %q", got)
	}
	if got := strField(m, "n"); got != "" {
		t.Errorf("non-string: want empty, got %q", got)
	}
	if got := strField(m, "b"); got != "" {
		t.Errorf("bool: want empty, got %q", got)
	}
	if got := strField(m, "nil"); got != "" {
		t.Errorf("nil: want empty, got %q", got)
	}
	if got := strField(m, "missing"); got != "" {
		t.Errorf("missing: want empty, got %q", got)
	}
}

func TestNewID_ReturnsValidUUID(t *testing.T) {
	seen := map[string]struct{}{}
	for i := 0; i < 100; i++ {
		id := newID()
		if len(id) != 36 {
			t.Fatalf("id %q has length %d, want 36", id, len(id))
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id: %s", id)
		}
		seen[id] = struct{}{}
	}
}
