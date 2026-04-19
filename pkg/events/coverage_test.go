package events

import (
	"strings"
	"testing"
)

// TestSubjectCoverage_AllPublishedSubjectsHaveStreams is the build gate
// for §3.2 / A3: if any subject in PublishedSubjects is not bound by a
// stream in DefaultStreams, `go test ./pkg/events/...` fails. The gate
// runs on every PR via .github/workflows/ci.yml (subjects-coverage job).
//
// Rationale: three separate staging incidents (audit subscribing `dms.>`,
// connector fanning out `dms.auth.*`, storage publishing
// `dms.version.uploaded.v1` to no-stream-bound) all presented as silent
// event loss — the broker accepts the publish and quietly discards it
// because nothing binds the subject. A 50-µs matcher check at test time
// catches it before merge.
func TestSubjectCoverage_AllPublishedSubjectsHaveStreams(t *testing.T) {
	missing := CheckCoverage(PublishedSubjects, DefaultStreams)
	if len(missing) == 0 {
		return
	}
	t.Fatalf(
		"the following published subjects are not covered by any stream "+
			"in DefaultStreams (add each to a StreamSpec.Subjects list):\n  %s\n"+
			"(or remove the subject from PublishedSubjects if it is no longer used)",
		strings.Join(missing, "\n  "),
	)
}

// TestSubjectCoverage_DeliberatelyUncoveredSubjectFails proves the negative
// case: a subject deliberately outside every stream is caught. This is
// the "fails the build on uncovered subject" criterion from the §3.2 / A3
// acceptance checklist — if a developer removes or reshuffles streams
// such that this subject becomes covered, this test starts passing a
// check it's supposed to fail, which itself is a regression. We pin it.
func TestSubjectCoverage_DeliberatelyUncoveredSubjectFails(t *testing.T) {
	fake := []string{"dms.totallyfake.doesnotexist.v1"}
	missing := CheckCoverage(fake, DefaultStreams)
	if len(missing) == 0 {
		t.Fatal("expected dms.totallyfake.doesnotexist.v1 to be uncovered; " +
			"if it is now covered, the coverage check is giving false positives")
	}
	if missing[0] != fake[0] {
		t.Fatalf("missing list wrong: got %v, want %v", missing, fake)
	}
}

// TestSubjectCovers_MatchingRules pins the wildcard semantics — easy to
// break accidentally during refactors. Each case reads as
// "filter covers subject: expected".
func TestSubjectCovers_MatchingRules(t *testing.T) {
	cases := []struct {
		filter, subj string
		want         bool
	}{
		// literal match
		{"dms.document.created.v1", "dms.document.created.v1", true},
		{"dms.document.created.v1", "dms.document.updated.v1", false},
		// single-token wildcard
		{"dms.*.created.v1", "dms.document.created.v1", true},
		{"dms.*.created.v1", "dms.document.deleted.v1", false},
		{"dms.*.created.v1", "dms.folder.created.v1", true},
		// tail wildcard
		{"dms.document.>", "dms.document.created.v1", true},
		{"dms.document.>", "dms.document.version.created.v1", true},
		{"dms.document.>", "dms.folder.created.v1", false},
		// tail wildcard does not match exactly one fewer token
		{"dms.document.>", "dms.document", false},
		// mismatched token counts
		{"dms.document.created", "dms.document.created.v1", false},
		{"dms.document.created.v1", "dms.document.created", false},
	}
	for _, tc := range cases {
		if got := SubjectCovers(tc.filter, tc.subj); got != tc.want {
			t.Errorf("SubjectCovers(%q, %q) = %v, want %v",
				tc.filter, tc.subj, got, tc.want)
		}
	}
}
