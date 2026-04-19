// Package fusion implements blueprint §7.1's hybrid-search score
// combiner: Reciprocal Rank Fusion (RRF) with the α weights
// specified in the architecture diagram (0.6 lexical, 0.4 semantic,
// k=60). Pure Go, no infrastructure dependencies — the search
// service orchestrates the two underlying queries and calls into
// this package to merge the results.
package fusion

import (
	"sort"
)

// Defaults per blueprint §7.1. k=60 is the canonical RRF constant
// from Cormack et al. 2009; tuning below 40 privileges top-ranked
// hits too aggressively and above 100 flattens the score curve.
const (
	DefaultK     = 60
	DefaultAlpha = 0.6 // lexical weight; semantic is (1 - α)
)

// RankedID pairs a document id with its 1-based rank in a single
// ranker's result list. Callers build one of these per ranker from
// the ordered hits.
type RankedID struct {
	ID   string
	Rank int // 1-based; smaller = more relevant
}

// Options tunes the fusion. Zero values select blueprint defaults.
type Options struct {
	K     int     // rank-discount constant
	Alpha float64 // lexical weight; semantic weight = 1 - Alpha
}

func (o Options) k() int {
	if o.K <= 0 {
		return DefaultK
	}
	return o.K
}

func (o Options) alpha() float64 {
	if o.Alpha <= 0 || o.Alpha > 1 {
		return DefaultAlpha
	}
	return o.Alpha
}

// Fuse combines a lexical and semantic result list per blueprint §7.1
// RRF:
//
//	score(d) = α · 1/(k + lex_rank(d)) + (1-α) · 1/(k + sem_rank(d))
//
// Documents appearing only in one list still score on that list's
// half; missing ranks contribute 0 to their side. The returned slice
// is sorted by score descending, then by ID ascending for stability.
// If `topN` is ≤ 0, every fused document is returned.
func Fuse(lex, sem []RankedID, opts Options) []Result {
	k := float64(opts.k())
	a := opts.alpha()

	scores := map[string]*Result{}
	add := func(list []RankedID, weight float64, isLex bool) {
		for _, it := range list {
			if it.ID == "" || it.Rank <= 0 {
				continue
			}
			r, ok := scores[it.ID]
			if !ok {
				r = &Result{ID: it.ID}
				scores[it.ID] = r
			}
			contribution := weight * 1.0 / (k + float64(it.Rank))
			r.Score += contribution
			if isLex {
				r.LexRank = it.Rank
			} else {
				r.SemRank = it.Rank
			}
		}
	}
	add(lex, a, true)
	add(sem, 1-a, false)

	out := make([]Result, 0, len(scores))
	for _, r := range scores {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Result is one fused hit. LexRank / SemRank retain per-ranker
// positions so the API layer can surface "matched in lexical only"
// vs "matched in both" (0 means absent from that ranker).
type Result struct {
	ID      string
	Score   float64
	LexRank int
	SemRank int
}
