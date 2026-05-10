package bulk

import (
	"testing"

	"github.com/google/uuid"

	vaultdmsv1 "github.com/vaultdms/vaultdms/proto/gen/go/vaultdms/v1"
)

// DigestItems should be deterministic across runs and stable to
// item-order changes (it's not — the JSON marshal preserves order).
// What we care about: identical input → identical output, distinct
// input → distinct output. The bulk service uses this to detect a
// request_id collision where the items don't match.
func TestDigestItems_DeterministicAndDistinguishing(t *testing.T) {
	items := []*vaultdmsv1.BulkItem{{
		Resource: &vaultdmsv1.BulkItem_Workspace{
			Workspace: &vaultdmsv1.BulkWorkspace{ExternalId: "W-1", Name: "Legal"},
		},
	}}
	a := DigestItems(items)
	b := DigestItems(items)
	if a == "" {
		t.Fatalf("digest empty")
	}
	if a != b {
		t.Fatalf("digest not deterministic: %s vs %s", a, b)
	}
	other := []*vaultdmsv1.BulkItem{{
		Resource: &vaultdmsv1.BulkItem_Workspace{
			Workspace: &vaultdmsv1.BulkWorkspace{ExternalId: "W-2", Name: "Finance"},
		},
	}}
	if DigestItems(other) == a {
		t.Fatalf("different items produced the same digest")
	}
}

func TestLtreeLabel_StripsAndPrefixes(t *testing.T) {
	id := uuid.MustParse("01976543-3210-7000-8000-000000000000")
	got := ltreeLabel("Legal Department / 2026", id)
	// Whitespace / "/" stripped; the 8-char id prefix appended.
	const want = "legal_department__2026_01976543"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// Empty name still produces a usable label.
	got2 := ltreeLabel("", id)
	if got2 != "f_01976543" {
		t.Fatalf("empty name: got %q", got2)
	}
}
