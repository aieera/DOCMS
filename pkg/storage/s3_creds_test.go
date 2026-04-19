package storage

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestResolveCreds documents the contract: both-or-neither.
// The chain branch is hard to test in isolation (it would hit IMDS); we
// only assert it returns non-nil when both inputs are empty.
func TestResolveCreds(t *testing.T) {
	t.Run("static when both present", func(t *testing.T) {
		c, err := resolveCreds("AKIA...", "secret")
		require.NoError(t, err)
		require.NotNil(t, c)
	})
	t.Run("chain when both empty", func(t *testing.T) {
		c, err := resolveCreds("", "")
		require.NoError(t, err)
		require.NotNil(t, c, "expected NewIAM chain")
	})
	t.Run("error when only access key", func(t *testing.T) {
		_, err := resolveCreds("AKIA...", "")
		require.Error(t, err)
	})
	t.Run("error when only secret key", func(t *testing.T) {
		_, err := resolveCreds("", "secret")
		require.Error(t, err)
	})
}
