// Task 6 (2026-07-28 task-service design) — unit coverage for
// parseMentions, the pure @mention-token parser comments.go's
// AddComment/UpdateComment build on. No DB, no ctx: same "pure function
// pinned by a table" pattern as transitions_test.go/permissions_test.go.
package service

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestParseMentions(t *testing.T) {
	a := uuid.Must(uuid.NewV7())
	b := uuid.Must(uuid.NewV7())

	cases := []struct {
		name string
		body string
		want []uuid.UUID
	}{
		{
			name: "no mentions",
			body: "just plain text, no tokens here",
			want: nil,
		},
		{
			name: "single token",
			body: "hey @[Alice](" + a.String() + ") take a look",
			want: []uuid.UUID{a},
		},
		{
			name: "multiple distinct tokens, order preserved",
			body: "@[Alice](" + a.String() + ") and @[Bob](" + b.String() + ") please review",
			want: []uuid.UUID{a, b},
		},
		{
			name: "duplicate token dedups to one, first occurrence order",
			body: "@[Bob](" + b.String() + ") ping @[Alice](" + a.String() + ") ping @[Bob again](" + b.String() + ")",
			want: []uuid.UUID{b, a},
		},
		{
			name: "malformed uuid (regex-shaped but not a real uuid) is dropped",
			body: "@[Bad](aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa)", // 36 hex chars, no dashes — not a valid UUID
			want: nil,
		},
		{
			name: "token too short to match the shape at all is dropped",
			body: "@[Bob](not-a-real-uuid)",
			want: nil,
		},
		{
			name: "missing brackets is not a token",
			body: "@Alice(" + a.String() + ") missing brackets",
			want: nil,
		},
		{
			name: "empty display name still parses",
			body: "@[](" + a.String() + ")",
			want: []uuid.UUID{a},
		},
		{
			name: "uppercase-hex uuid still parses (case-insensitive)",
			body: "@[Alice](" + strings.ToUpper(a.String()) + ")",
			want: []uuid.UUID{a},
		},
		{
			name: "two mentions of the same user in different sentences still dedups",
			body: "@[Alice](" + a.String() + ") first, then later @[Alice](" + a.String() + ") again",
			want: []uuid.UUID{a},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseMentions(c.body)
			if len(got) != len(c.want) {
				t.Fatalf("parseMentions(%q) = %v, want %v", c.body, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("parseMentions(%q)[%d] = %v, want %v", c.body, i, got[i], c.want[i])
				}
			}
		})
	}
}
