package service

import "github.com/google/uuid"

// newExternalID returns a UUID suitable for any identifier that will
// appear in a URL, API response, or anywhere else a client (or an
// adversary) can observe it.
//
// We use v4 (random) specifically to avoid the timestamp leakage and
// enumeration ordering of v7. UUIDv7's first 48 bits encode the wall-
// clock millisecond of creation; for client-facing IDs that lets an
// attacker (a) recover document/folder creation times by parsing the
// id alone, and (b) feasibly enumerate adjacent IDs by guessing
// nearby timestamps + the 74-bit random tail. v4 has 122 bits of
// entropy and reveals nothing about creation time.
//
// Internal IDs (outbox event_id, hash chain seed, etc.) can keep
// using uuid.NewV7() — they're never URL-bound and benefit from
// B-tree locality on insert. The contract here is purely "is this id
// going to appear in something a non-author can see?".
//
// FIX-3 (2026-05-31). Audit C3 — UUIDv7 timestamp leakage on
// document/folder/workspace/version/share-link/comment/annotation/task
// IDs.
func newExternalID() (uuid.UUID, error) {
	return uuid.NewRandom()
}
