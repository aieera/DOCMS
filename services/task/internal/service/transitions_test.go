// Task 5 (2026-07-28 task-service design) — table-driven unit coverage
// for transitions.go's pure validTransition matrix. No DB, no ctx: this
// pins down the exact from/to pairs the brief's four transition methods
// (StartTask/CompleteTask/ReopenTask/CancelTask) rely on to gate illegal
// moves before touching the database.
package service

import "testing"

// TestValidTransition_Matrix enumerates every from/to combination across
// the four known statuses (open, in_progress, done, cancelled) plus a
// couple of garbage values, and asserts validTransition agrees with the
// brief's matrix:
//
//	open        -> in_progress, done, cancelled
//	in_progress -> done, cancelled
//	done        -> open
//	cancelled   -> open
//
// Same-state "transitions" (e.g. open->open) are illegal — a transition
// method always moves status, it never no-ops.
func TestValidTransition_Matrix(t *testing.T) {
	valid := map[[2]string]bool{
		{"open", "in_progress"}:      true,
		{"open", "done"}:             true,
		{"open", "cancelled"}:        true,
		{"in_progress", "done"}:      true,
		{"in_progress", "cancelled"}: true,
		{"done", "open"}:             true,
		{"cancelled", "open"}:        true,
	}

	statuses := []string{"open", "in_progress", "done", "cancelled"}
	for _, from := range statuses {
		for _, to := range statuses {
			from, to := from, to
			want := valid[[2]string{from, to}]
			t.Run(from+"->"+to, func(t *testing.T) {
				if got := validTransition(from, to); got != want {
					t.Errorf("validTransition(%q, %q) = %v, want %v", from, to, got, want)
				}
			})
		}
	}

	// A few explicit illegal pairs called out by the brief, plus garbage
	// input, so the matrix's edges are pinned down by name rather than
	// only by the exhaustive loop above.
	illegal := [][2]string{
		{"in_progress", "open"}, // only done/cancelled can reopen
		{"done", "in_progress"}, // done can only reopen, not resume
		{"cancelled", "done"},   // cancelled can only reopen, not complete
		{"done", "cancelled"},   // done can only reopen
		{"", "open"},
		{"open", "bogus"},
		{"bogus", "open"},
	}
	for _, pair := range illegal {
		from, to := pair[0], pair[1]
		t.Run("illegal_"+from+"->"+to, func(t *testing.T) {
			if validTransition(from, to) {
				t.Errorf("validTransition(%q, %q) = true, want false", from, to)
			}
		})
	}
}
