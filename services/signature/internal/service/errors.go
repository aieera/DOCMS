package service

import "errors"

// Sentinel errors the handlers map to HTTP status codes. Wrap them with
// fmt.Errorf("...: %w", Err...) to add context; never compare by string.
var (
	// ErrRequestNotFound — the request (or session) does not exist for the
	// caller's tenant. → 404
	ErrRequestNotFound = errors.New("signature request not found")
	// ErrSignerNotFound — the signer id does not belong to the request. → 404
	ErrSignerNotFound = errors.New("signer not found on this request")
	// ErrInvalidSigningToken — missing or wrong per-signer token. → 401
	ErrInvalidSigningToken = errors.New("invalid or missing signing token")
	// ErrRequestNotSignable — the request is canceled, expired, declined or
	// already completed, so no further signing, sending or canceling. → 409
	ErrRequestNotSignable = errors.New("signature request is not open for signing")
	// ErrAlreadySigned — the signer already signed (or declined). → 409
	ErrAlreadySigned = errors.New("signer has already signed")
	// ErrForbidden — the caller is authenticated but not allowed. → 403
	ErrForbidden = errors.New("forbidden")
	// ErrInvalidRequest — the call is malformed or inconsistent with the
	// stored request (e.g. provider mismatch). → 400
	ErrInvalidRequest = errors.New("invalid request")
	// ErrUpstream — the e-sign vendor / QTSP (or its connection or
	// configuration) failed. → 502
	ErrUpstream = errors.New("e-sign provider error")
)
