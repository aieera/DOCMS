package service

import (
	"hash/fnv"
	"strings"
)

// Customer-folder bucketing (Workstream 6 — folder hierarchy at 100k).
//
// A naive integration that creates one folder per customer directly under a
// single "Customers" root would pile 100k+ direct children under one parent —
// exactly the ListByParent hot spot keyset pagination mitigates on read, but it
// still makes the tree unwieldy. PlaceCustomerFolder shards customers under
// deterministic intermediate buckets so no single parent holds more than a few
// hundred direct children.
//
// The helper is deterministic + pure (same input → same buckets) so the future
// ingestion path can compute the target parent without coordination: ensure the
// returned bucket folders exist (CreateFolder is idempotent on name+parent),
// then create the customer folder under the deepest one.

// Bucket modes.
const (
	// BucketModeHash spreads customers uniformly via an FNV hash prefix —
	// the default, robust to skewed/sequential customer ids.
	BucketModeHash = "hash"
	// BucketModeIDPrefix buckets by the leading characters of the (slugged)
	// customer ref. Cheap + human-readable, but only even when ids are.
	BucketModeIDPrefix = "id_prefix"
	// BucketModeRegion buckets by region first, then a hash sub-bucket.
	BucketModeRegion = "region"
)

const defaultBucketPrefixLen = 2

// FolderBucketScheme configures customer-folder sharding. PrefixLen is the bucket
// label width: hash mode with PrefixLen=2 yields 256 buckets (≈400 customers per
// bucket at 100k), PrefixLen=3 yields 4096.
type FolderBucketScheme struct {
	Mode      string
	PrefixLen int
}

// FolderBucketScheme returns the effective scheme, applying defaults (hash / 2).
func (s *DocumentService) FolderBucketScheme() FolderBucketScheme {
	return normalizeBucketScheme(s.folderBuckets)
}

// PlaceCustomerFolder returns the ordered intermediate bucket labels under which
// a customer folder should be created, per the configured scheme. The labels are
// ltree-safe ([a-z0-9_]). Deterministic for a given (customerRef, region).
//
// Example (hash/2): PlaceCustomerFolder("CUST-10042", "") → ["a3"], so the
// integration files the customer at <root>/a3/CUST-10042 rather than
// <root>/CUST-10042.
func (s *DocumentService) PlaceCustomerFolder(customerRef, region string) []string {
	return computeBuckets(s.FolderBucketScheme(), customerRef, region)
}

func normalizeBucketScheme(in FolderBucketScheme) FolderBucketScheme {
	out := in
	switch out.Mode {
	case BucketModeHash, BucketModeIDPrefix, BucketModeRegion:
	default:
		out.Mode = BucketModeHash
	}
	if out.PrefixLen <= 0 {
		out.PrefixLen = defaultBucketPrefixLen
	}
	if out.PrefixLen > 8 {
		out.PrefixLen = 8 // an 8-hex-char bucket is already 4B buckets; cap it.
	}
	return out
}

func computeBuckets(scheme FolderBucketScheme, customerRef, region string) []string {
	scheme = normalizeBucketScheme(scheme)
	switch scheme.Mode {
	case BucketModeIDPrefix:
		return []string{idPrefixBucket(customerRef, scheme.PrefixLen)}
	case BucketModeRegion:
		top := bucketSlug(region)
		if top == "" {
			top = "global"
		}
		return []string{top, hashBucket(customerRef, scheme.PrefixLen)}
	default: // BucketModeHash
		return []string{hashBucket(customerRef, scheme.PrefixLen)}
	}
}

// hashBucket returns the first prefixLen hex chars of FNV-32a(customerRef) — a
// stable, uniform bucket label.
func hashBucket(customerRef string, prefixLen int) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.ToLower(strings.TrimSpace(customerRef))))
	hexStr := leftPadHex(h.Sum32())
	if prefixLen > len(hexStr) {
		prefixLen = len(hexStr)
	}
	return hexStr[:prefixLen]
}

// idPrefixBucket slugs the ref to ltree-safe chars and takes the leading
// prefixLen of them. Falls back to a single underscore for an empty/odd ref.
func idPrefixBucket(customerRef string, prefixLen int) string {
	slug := bucketSlug(customerRef)
	if slug == "" {
		return "_"
	}
	if prefixLen > len(slug) {
		prefixLen = len(slug)
	}
	return slug[:prefixLen]
}

// leftPadHex renders a uint32 as a fixed 8-char lowercase hex string.
func leftPadHex(v uint32) string {
	const hexdigits = "0123456789abcdef"
	var b [8]byte
	for i := 7; i >= 0; i-- {
		b[i] = hexdigits[v&0xf]
		v >>= 4
	}
	return string(b[:])
}

// bucketSlug lowercases + keeps [a-z0-9_] (mapping space/-/. to _), matching the
// ltree label alphabet.
func bucketSlug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_' || r == '.':
			b.WriteRune('_')
		}
	}
	return b.String()
}
