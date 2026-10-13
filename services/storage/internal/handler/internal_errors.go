package handler

import (
	"errors"
	"net/http"

	zlog "github.com/rs/zerolog/log"

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

// writeInternalErr answers a failed internal admin call with the status
// from internalErrStatus and a body that never carries internal detail: a
// 5xx is a fixed "internal error" (the error, which can name KMS keys,
// buckets or DB state, is logged); a 4xx is the domain error's own Message
// — the text the document service surfaces — without its wrapped cause.
func writeInternalErr(w http.ResponseWriter, r *http.Request, op string, err error) {
	status := internalErrStatus(err)
	msg := "internal error"
	if status >= http.StatusInternalServerError {
		zlog.Error().Err(err).Str("op", op).Str("path", r.URL.Path).Msg("internal endpoint failed")
	} else {
		var de *vdmserr.Error
		if errors.As(err, &de) && de.Message != "" {
			msg = de.Message
		}
		zlog.Warn().Err(err).Str("op", op).Int("status", status).Msg("internal endpoint refused")
	}
	http.Error(w, msg, status)
}
