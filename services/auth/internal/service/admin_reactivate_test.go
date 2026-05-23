// Phase 4 — reactivate state-machine pin.
//
// ReactivateUser the full path needs a real Postgres tx + outbox publisher
// and lives in the integration suite. The pre-tx state check (which is
// the only place the call refuses input) is pure and we pin it here so
// future refactors can't accidentally widen the allowed inputs.
package service

import (
	"testing"

	"github.com/stretchr/testify/require"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/auth/internal/model"
)

func TestValidateReactivableStatus(t *testing.T) {
	t.Run("suspended is reactivable", func(t *testing.T) {
		require.NoError(t, validateReactivableStatus(model.StatusSuspended))
	})

	t.Run("active is rejected (would be a no-op event)", func(t *testing.T) {
		err := validateReactivableStatus(model.StatusActive)
		require.Error(t, err)
		require.Equal(t, vdmserr.KindValidation, vdmserr.KindOf(err))
	})

	t.Run("deactivated must go through the compliance restore path", func(t *testing.T) {
		err := validateReactivableStatus(model.StatusDeactivated)
		require.Error(t, err)
		require.Equal(t, vdmserr.KindValidation, vdmserr.KindOf(err))
	})

	t.Run("unknown future state refuses by default (fail-closed)", func(t *testing.T) {
		err := validateReactivableStatus(model.Status("frozen"))
		require.Error(t, err)
	})
}
