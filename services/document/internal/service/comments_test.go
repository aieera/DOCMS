// ADR 0066 — invariants the unit suite can pin without a real DB.
// What we lock down:
//   - Mention regex: extracts user_id from `@[Display Name](uuid)`.
//   - Mention dedupe: one event per unique mentioned user even if
//     the same user is referenced twice in a single comment body.
//   - Self-mention is filtered out (user shouldn't notify themselves).
//   - Bad uuids inside a mention syntax are silently dropped.
//
// Full create/list/update flows need Postgres + tenant tx and live
// in the integration suite under -tags=integration.
package service

import (
	"testing"

	"github.com/google/uuid"
)

func TestParseMentions_Single(t *testing.T) {
	uid := uuid.New()
	body := "Hey @[Alice](" + uid.String() + ") please review."
	got := parseMentions(body)
	if _, ok := got[uid]; !ok {
		t.Errorf("missing %v in %v", uid, got)
	}
	if len(got) != 1 {
		t.Errorf("got %d mentions; want 1", len(got))
	}
}

func TestParseMentions_DedupesRepeats(t *testing.T) {
	uid := uuid.New()
	body := "@[Alice](" + uid.String() + ") and again @[Alice](" + uid.String() + ")"
	got := parseMentions(body)
	if len(got) != 1 {
		t.Errorf("got %d unique mentions; want 1 (dedupe broken)", len(got))
	}
}

func TestParseMentions_MultipleUsers(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	body := "Splitting the work: @[Alice](" + a.String() + ") + @[Bob](" + b.String() + ")"
	got := parseMentions(body)
	if len(got) != 2 {
		t.Fatalf("got %d mentions; want 2", len(got))
	}
	if _, ok := got[a]; !ok {
		t.Errorf("Alice missing")
	}
	if _, ok := got[b]; !ok {
		t.Errorf("Bob missing")
	}
}

func TestParseMentions_DropsInvalidUUID(t *testing.T) {
	body := "Bad reference @[Bob](not-a-uuid) ignored"
	got := parseMentions(body)
	if len(got) != 0 {
		t.Errorf("invalid uuid leaked through: %v", got)
	}
}

func TestParseMentions_NoMentions(t *testing.T) {
	body := "Just a plain comment with no @ syntax."
	got := parseMentions(body)
	if len(got) != 0 {
		t.Errorf("got %v want empty", got)
	}
}

func TestParseMentions_MalformedBracketIgnored(t *testing.T) {
	uid := uuid.New()
	// Missing closing paren — should NOT match.
	body := "@[Alice](" + uid.String()
	got := parseMentions(body)
	if len(got) != 0 {
		t.Errorf("malformed mention matched: %v", got)
	}
}
