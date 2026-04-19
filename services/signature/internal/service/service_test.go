package service

import (
	"regexp"
	"testing"
)

// The service is built around transactional outbox writes that require real
// Postgres. These tests focus on the pure helpers — generateToken (used in
// every sign-URL) and the repository's NewID (used as the row id).

func TestGenerateToken_HexLengthAndUniqueness(t *testing.T) {
	seen := map[string]struct{}{}
	re := regexp.MustCompile(`^[0-9a-f]{32}$`)
	for i := 0; i < 50; i++ {
		tok := generateToken()
		if !re.MatchString(tok) {
			t.Fatalf("token %q doesn't match 32-hex-char pattern", tok)
		}
		if _, dup := seen[tok]; dup {
			t.Fatalf("duplicate token: %s", tok)
		}
		seen[tok] = struct{}{}
	}
}

func TestGenerateToken_NonDeterministic(t *testing.T) {
	// Belt-and-braces: if generateToken ever switches to a deterministic
	// seed (e.g. accidentally swaps to math/rand.Read), 50 draws collapse
	// to 1 unique value and this assertion fires.
	set := map[string]struct{}{}
	for i := 0; i < 50; i++ {
		set[generateToken()] = struct{}{}
	}
	if len(set) < 50 {
		t.Fatalf("got only %d unique tokens out of 50 — non-random source?", len(set))
	}
}
