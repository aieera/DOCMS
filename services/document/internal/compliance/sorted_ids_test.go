package compliance

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// The binding loops lock documents in sortedIDs order; the order must be
// total and must not reorder the caller's slice.
func TestSortedIDs_FixedOrderWithoutMutatingInput(t *testing.T) {
	a := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	b := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	c := uuid.MustParse("ffffffff-0000-0000-0000-000000000000")
	in := []uuid.UUID{c, a, b}
	require.Equal(t, []uuid.UUID{a, b, c}, sortedIDs(in))
	require.Equal(t, []uuid.UUID{c, a, b}, in)
}
