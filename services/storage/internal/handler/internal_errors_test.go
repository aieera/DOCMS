package handler

import (
	"errors"
	"fmt"
	"net/http"
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
