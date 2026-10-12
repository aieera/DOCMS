package handler

import (
	"net/http"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
)

// internalErrStatus maps a service error to the HTTP status the internal
// admin endpoints (reencrypt-blob, rewrap-dek, worm-lock) answer with, so
// callers can tell a retryable lost race (409) or a refused request
// (400/404/422) from a server fault (500).
func internalErrStatus(err error) int {
	switch vdmserr.KindOf(err) {
	case vdmserr.KindValidation:
		return http.StatusBadRequest
	case vdmserr.KindNotFound:
		return http.StatusNotFound
	case vdmserr.KindConflict:
		return http.StatusConflict
	case vdmserr.KindRegionViolation:
		return http.StatusUnprocessableEntity
	default:
		return http.StatusInternalServerError
	}
}
