package handler

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
)

func TestInternalErrStatus(t *testing.T) {
	require.Equal(t, http.StatusConflict, internalErrStatus(fmt.Errorf("update blob envelope: %w", vdmserr.Conflict("changed"))))
	require.Equal(t, http.StatusNotFound, internalErrStatus(fmt.Errorf("load blob: %w", vdmserr.ErrNotFound)))
	require.Equal(t, http.StatusBadRequest, internalErrStatus(vdmserr.Validation("retain_until", "bad")))
	require.Equal(t, http.StatusUnprocessableEntity, internalErrStatus(vdmserr.ErrRegionViolation))
	require.Equal(t, http.StatusInternalServerError, internalErrStatus(errors.New("s3 down")))
}

// TestWriteInternalErr_NoInternalDetail: the internal admin endpoints
// (reencrypt-blob, rewrap-dek, worm-lock) used to answer err.Error(), which
// carried KMS / S3 / DB detail on a 500 and the wrapped vendor cause on a
// 4xx. A 5xx body is now generic; a 4xx body is the domain error's own
// message (what the document service surfaces), without its cause.
func TestWriteInternalErr_NoInternalDetail(t *testing.T) {
	t.Run("5xx is generic", func(t *testing.T) {
		rec := httptest.NewRecorder()
		writeInternalErr(rec, httptest.NewRequest(http.MethodPost, "/internal/v1/rewrap-dek", nil), "rewrap-dek",
			fmt.Errorf("unwrap DEK: %w", errors.New("kms: AccessDeniedException arn:aws:kms:eu-central-1:123456789012:key/abc")))
		require.Equal(t, http.StatusInternalServerError, rec.Code)
		require.NotContains(t, rec.Body.String(), "arn:aws")
		require.NotContains(t, rec.Body.String(), "unwrap DEK")
		require.Equal(t, "internal error\n", rec.Body.String())
	})
	t.Run("4xx keeps the domain message, drops the cause", func(t *testing.T) {
		rec := httptest.NewRecorder()
		err := vdmserr.Wrap(vdmserr.Conflict("retention cannot be shortened"),
			errors.New("s3: InvalidRequest bucket sedoc-eu-central-1 object tenants/x/blob"))
		writeInternalErr(rec, httptest.NewRequest(http.MethodPost, "/internal/v1/worm-lock", nil), "worm-lock",
			fmt.Errorf("apply worm: %w", err))
		require.Equal(t, http.StatusConflict, rec.Code)
		require.Equal(t, "retention cannot be shortened\n", rec.Body.String())
	})
}
