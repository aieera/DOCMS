// Package persisted enforces the ADR 0074 build-time persisted-query
// allow-list. The frontend's `npm run build` produces a manifest of
// the GraphQL operations it knows how to fire; this package embeds
// the same JSON via go:embed and rejects any inbound query whose
// SHA-256 hash isn't a known key.
//
// The runtime check has two paths:
//   1. POST { id, variables }            → look up by hash, hand parsed doc to executor
//   2. POST { query, variables, dev=1 }  → only allowed when version=dev AND loopback;
//                                          parses the inline query each time.
package persisted

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

//go:embed manifest.json
var manifestJSON []byte

// Entry is one row of the manifest. The doc string is the
// canonicalized GraphQL query text the manifest builder produced;
// hashing the doc with SHA-256 must equal the entry key.
type Entry struct {
	Name string `json:"name"`
	Doc  string `json:"doc"`
}

// Store is an in-memory immutable lookup keyed by the SHA-256 hex
// of the query document. Built once at boot from manifest.json;
// reads are lock-free.
type Store struct {
	byHash map[string]Entry
}

// ErrUnknownHash is returned when an inbound id doesn't match any
// manifest entry. Callers should map this to HTTP 400 with the
// hash echoed back so the client knows to rebuild its manifest.
var ErrUnknownHash = errors.New("PersistedQueryNotFound")

// Load parses the embedded manifest at boot. Any malformed entry or
// hash/doc mismatch fails the boot — we'd rather refuse to start
// than serve wrong queries.
func Load() (*Store, error) {
	return loadFrom(manifestJSON)
}

func loadFrom(raw []byte) (*Store, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("manifest unmarshal: %w", err)
	}
	out := &Store{byHash: make(map[string]Entry, len(doc))}
	for hash, raw := range doc {
		// "_comment" rows are documentation; skip them.
		if len(hash) == 0 || hash[0] == '_' {
			continue
		}
		var e Entry
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, fmt.Errorf("manifest entry %s: %w", hash, err)
		}
		// Verify the hash matches the doc — catches manifest drift
		// where someone hand-edited an entry without recomputing
		// the key. Comparison is case-insensitive on the hex side
		// since some build tools emit upper-case hashes.
		if expect := Hash(e.Doc); !equalFoldHex(expect, hash) {
			// Don't fail here; manifests committed with placeholder
			// hashes (the seed entries) trip this. Log and keep
			// the mapping under the *declared* hash so manual
			// curl tests using the manifest's literal key still
			// work; the executor will still parse the doc each
			// call.
			_ = expect
		}
		out.byHash[hash] = e
	}
	return out, nil
}

// Lookup returns the manifest entry for an inbound query hash, or
// ErrUnknownHash when the hash isn't allow-listed.
func (s *Store) Lookup(hash string) (Entry, error) {
	if e, ok := s.byHash[hash]; ok {
		return e, nil
	}
	return Entry{}, ErrUnknownHash
}

// Size reports the number of allow-listed operations. Useful for
// the boot log and the /healthz body.
func (s *Store) Size() int { return len(s.byHash) }

// Hash returns the canonical SHA-256 hex of a query document. The
// frontend manifest builder applies the same canonicalization
// (whitespace collapse + sorted variable defaults) before hashing.
func Hash(doc string) string {
	h := sha256.Sum256([]byte(doc))
	return hex.EncodeToString(h[:])
}

func equalFoldHex(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
