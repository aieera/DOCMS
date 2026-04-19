package fusion

import (
	"math"
	"testing"
)

// TestFuse_BlueprintFormula pins the exact RRF formula from §7.1.
// A regression here means the search relevance contract just moved
// without the architecture signing off.
func TestFuse_BlueprintFormula(t *testing.T) {
	lex := []RankedID{{ID: "doc-A", Rank: 1}, {ID: "doc-B", Rank: 2}}
	sem := []RankedID{{ID: "doc-B", Rank: 1}, {ID: "doc-C", Rank: 2}}
	got := Fuse(lex, sem, Options{}) // defaults: α=0.6, k=60

	// Expected (α=0.6, k=60):
	//   doc-A: 0.6/61 +   0   = 0.009836...
	//   doc-B: 0.6/62 + 0.4/61 = 0.009677... + 0.006557... = 0.016234...
	//   doc-C:   0   + 0.4/62 = 0.006451...
	want := map[string]float64{
		"doc-A": 0.6/61 + 0,
		"doc-B": 0.6/62 + 0.4/61,
		"doc-C": 0 + 0.4/62,
	}
	for _, r := range got {
		if !approxEq(r.Score, want[r.ID]) {
			t.Errorf("%s score = %.10f, want %.10f", r.ID, r.Score, want[r.ID])
		}
	}

	// Order: doc-B (both rankers) > doc-A (lex only) > doc-C (sem only).
	wantOrder := []string{"doc-B", "doc-A", "doc-C"}
	for i, id := range wantOrder {
		if got[i].ID != id {
			t.Errorf("position %d = %s, want %s", i, got[i].ID, id)
		}
	}
}

// A doc appearing only in semantic still scores — but LESS than a doc
// appearing only in lexical at the same rank, because α=0.6 > 0.4.
// This is the "lexical-biased" property of the blueprint's weighting
// choice; callers should rely on it.
func TestFuse_LexicalWeightDominatesAtEqualRank(t *testing.T) {
	lexOnly := Fuse([]RankedID{{"only-lex", 1}}, nil, Options{})
	semOnly := Fuse(nil, []RankedID{{"only-sem", 1}}, Options{})
	if lexOnly[0].Score <= semOnly[0].Score {
		t.Errorf("α=0.6 should weight lex > sem at equal rank; got lex=%.6f sem=%.6f",
			lexOnly[0].Score, semOnly[0].Score)
	}
}

// Ties on score → tie-breaker is ID ascending (deterministic paging).
func TestFuse_DeterministicTieBreak(t *testing.T) {
	// Two docs tied at rank 1 in lex-only; same score.
	lex := []RankedID{{"doc-b", 1}, {"doc-a", 1}}
	got := Fuse(lex, nil, Options{})
	if got[0].ID != "doc-a" || got[1].ID != "doc-b" {
		t.Errorf("tie-break should be ID-ascending; got %s, %s", got[0].ID, got[1].ID)
	}
}

// Invalid rank (≤0) or empty ID: silently skipped. Upstream rankers
// sometimes emit placeholder rows; they must not distort the fusion.
func TestFuse_IgnoresZeroAndNegativeRanks(t *testing.T) {
	lex := []RankedID{{"doc-a", 1}, {"", 2}, {"doc-b", 0}, {"doc-c", -5}}
	got := Fuse(lex, nil, Options{})
	if len(got) != 1 || got[0].ID != "doc-a" {
		t.Errorf("expected only doc-a, got %+v", got)
	}
}

// Defaults are substituted only for out-of-range values — explicit
// α=0.5 and k=10 must be honored.
func TestFuse_CustomOptions(t *testing.T) {
	lex := []RankedID{{"d1", 1}}
	sem := []RankedID{{"d1", 1}}
	got := Fuse(lex, sem, Options{K: 10, Alpha: 0.5})
	// 0.5/11 + 0.5/11 = 1/11.
	want := 1.0 / 11
	if !approxEq(got[0].Score, want) {
		t.Errorf("custom-opts score = %.10f, want %.10f", got[0].Score, want)
	}
}

// LexRank / SemRank are preserved for the response payload.
func TestFuse_PreservesPerRankerRanks(t *testing.T) {
	lex := []RankedID{{"doc-a", 3}}
	sem := []RankedID{{"doc-a", 7}}
	got := Fuse(lex, sem, Options{})
	if got[0].LexRank != 3 || got[0].SemRank != 7 {
		t.Errorf("ranks lost: lex=%d sem=%d", got[0].LexRank, got[0].SemRank)
	}
}

func approxEq(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
