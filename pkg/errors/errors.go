// Package errors defines VaultDMS domain errors and the translation rules
// between them and gRPC / HTTP wire errors. Handlers return domain errors;
// transport layers call ToGRPCError / ToHTTPError at the edge.
package errors

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Kind enumerates high-level error categories used for wire-level translation.
type Kind int

const (
	KindUnknown Kind = iota
	KindNotFound
	KindAlreadyExists
	KindForbidden
	KindUnauthorized
	KindValidation
	KindConflict
	KindRateLimited
	KindRegionViolation
	KindLegalHold
	KindUnavailable // dependent service (virus scanner, policy, KMS) is down
	KindPayloadTooLarge
	KindMIMEMismatch // server-detected MIME disagrees with client Content-Type (409)
	KindInternal
)

// Error is the base domain error. Callers construct concrete errors via the
// helpers below and wrap them with fmt.Errorf / errors.Join freely.
type Error struct {
	Kind    Kind
	Code    string
	Message string
	Field   string
	// Details carries error-kind-specific structured data that can't be
	// flattened into Message (e.g. declared vs detected MIME on a
	// MIME_MISMATCH). Survives both ToGRPCError (JSON-encoded into the
	// status message) and ToHTTPError (copied to the JSON body).
	Details map[string]any
	Cause   error
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Cause }

// Sentinel errors allow callers to use errors.Is / errors.As naturally.
var (
	ErrNotFound        = &Error{Kind: KindNotFound, Code: "NOT_FOUND", Message: "resource not found"}
	ErrAlreadyExists   = &Error{Kind: KindAlreadyExists, Code: "ALREADY_EXISTS", Message: "resource already exists"}
	ErrForbidden       = &Error{Kind: KindForbidden, Code: "FORBIDDEN", Message: "permission denied"}
	ErrUnauthorized    = &Error{Kind: KindUnauthorized, Code: "UNAUTHORIZED", Message: "authentication required"}
	ErrRateLimited     = &Error{Kind: KindRateLimited, Code: "RATE_LIMITED", Message: "rate limit exceeded"}
	ErrRegionViolation = &Error{Kind: KindRegionViolation, Code: "REGION_VIOLATION", Message: "region policy violation"}
	ErrLegalHold       = &Error{Kind: KindLegalHold, Code: "LEGAL_HOLD", Message: "document is under legal hold"}
	ErrInternal        = &Error{Kind: KindInternal, Code: "INTERNAL", Message: "internal error"}
)

// Validation constructs a validation error for a single field.
func Validation(field, message string) *Error {
	return &Error{Kind: KindValidation, Code: "INVALID_ARGUMENT", Field: field, Message: message}
}

// Conflict constructs a conflict / failed-precondition error.
func Conflict(reason string) *Error {
	return &Error{Kind: KindConflict, Code: "CONFLICT", Message: reason}
}

// NotFound constructs a not-found error with a specific message.
func NotFound(message string) *Error {
	return &Error{Kind: KindNotFound, Code: "NOT_FOUND", Message: message}
}

// Forbidden constructs a permission-denied error with a specific message.
func Forbidden(message string) *Error {
	return &Error{Kind: KindForbidden, Code: "FORBIDDEN", Message: message}
}

// Unauthorized constructs an authentication-required error with a specific
// message. Maps to HTTP 401 / gRPC Unauthenticated.
func Unauthorized(message string) *Error {
	return &Error{Kind: KindUnauthorized, Code: "UNAUTHORIZED", Message: message}
}

// Internal constructs an internal error with an operator-visible message.
// The message is scrubbed on the wire by ToHTTPError / ToGRPCError.
func Internal(message string) *Error {
	return &Error{Kind: KindInternal, Code: "INTERNAL", Message: message}
}

// Unavailable signals that a hard dependency (virus scanner, policy, KMS)
// cannot service the request. Wire translation: gRPC Unavailable / HTTP 503.
// Used by the fail-closed path on CompleteUpload when ClamAV is unreachable.
func Unavailable(message string) *Error {
	return &Error{Kind: KindUnavailable, Code: "UNAVAILABLE", Message: message}
}

// PayloadTooLarge signals that a request body (upload size) exceeds the
// tenant's plan limit. Wire translation: HTTP 413. gRPC has no direct
// analogue so ResourceExhausted is used.
func PayloadTooLarge(message string) *Error {
	return &Error{Kind: KindPayloadTooLarge, Code: "PAYLOAD_TOO_LARGE", Message: message}
}

// MIMEMismatch signals that the server-detected MIME disagrees with the
// client-declared Content-Type badly enough to reject the upload (typical
// case: deny-listed exe type behind a benign extension). Wire: HTTP 409
// with a structured body that names both MIME values so the uploader can
// render an actionable inline alert. Blueprint §22 anti-pattern #6.
func MIMEMismatch(declared, detected string) *Error {
	return &Error{
		Kind:    KindMIMEMismatch,
		Code:    "MIME_MISMATCH",
		Message: fmt.Sprintf("file type doesn't match extension: declared %s, detected %s", declared, detected),
		Details: map[string]any{
			"declared_mime": declared,
			"detected_mime": detected,
		},
	}
}

// Wrap attaches a cause to a domain error, preserving its Kind and Code.
func Wrap(base *Error, cause error) *Error {
	if base == nil {
		return &Error{Kind: KindInternal, Code: "INTERNAL", Message: "internal error", Cause: cause}
	}
	cp := *base
	cp.Cause = cause
	return &cp
}

// wirePayload is the JSON envelope we smuggle through a gRPC status
// message so the HTTP proxy on the other side can reconstruct the
// original domain error shape (type/message/details). Callers should
// never read from this directly — it's an implementation detail of the
// gRPC↔HTTP edge.
type wirePayload struct {
	Type    string         `json:"type"`
	Message string         `json:"message"`
	Field   string         `json:"field,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

// EncodeWire returns a compact JSON string suitable for embedding in a
// gRPC status message. Decoded on the receiving side by DecodeWire.
func (e *Error) EncodeWire() string {
	p := wirePayload{Type: e.Code, Message: e.Message, Field: e.Field, Details: e.Details}
	b, err := json.Marshal(p)
	if err != nil {
		return e.Message
	}
	return string(b)
}

// DecodeWire parses a gRPC status message that may be an EncodeWire JSON
// envelope. Returns ok=true only when the message parses AND names a
// known error code.
func DecodeWire(msg string) (wirePayload, bool) {
	msg = strings.TrimSpace(msg)
	if !strings.HasPrefix(msg, "{") {
		return wirePayload{}, false
	}
	var p wirePayload
	if err := json.Unmarshal([]byte(msg), &p); err != nil {
		return wirePayload{}, false
	}
	if p.Type == "" {
		return wirePayload{}, false
	}
	return p, true
}

// ---- Classification --------------------------------------------------------

// KindOf reports the Kind of err, looking through wraps. Returns KindUnknown
// for nil / non-domain errors.
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return KindUnknown
}

// FromPgError maps a pgx / pgconn error to a domain error. Unique violation
// becomes ErrAlreadyExists; no-rows becomes ErrNotFound; foreign-key
// violation becomes Conflict. Other errors are wrapped as Internal.
func FromPgError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return Wrap(ErrNotFound, err)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return Wrap(ErrAlreadyExists, err)
		case "23503": // foreign_key_violation
			return Wrap(Conflict("foreign key violation"), err)
		case "23514": // check_violation
			return Wrap(Validation("", pgErr.Message), err)
		case "23502": // not_null_violation
			return Wrap(Validation(pgErr.ColumnName, "field is required"), err)
		}
	}
	return Wrap(ErrInternal, err)
}

// ---- gRPC translation ------------------------------------------------------

// ToGRPCError maps a domain error to a gRPC status error.
func ToGRPCError(err error) error {
	if err == nil {
		return nil
	}
	var e *Error
	if !errors.As(err, &e) {
		return status.Error(codes.Internal, err.Error())
	}
	switch e.Kind {
	case KindNotFound:
		return status.Error(codes.NotFound, e.Message)
	case KindAlreadyExists:
		return status.Error(codes.AlreadyExists, e.Message)
	case KindForbidden:
		return status.Error(codes.PermissionDenied, e.Message)
	case KindUnauthorized:
		return status.Error(codes.Unauthenticated, e.Message)
	case KindValidation:
		return status.Error(codes.InvalidArgument, e.Error())
	case KindConflict, KindLegalHold:
		return status.Error(codes.FailedPrecondition, e.Message)
	case KindRateLimited:
		return status.Error(codes.ResourceExhausted, e.Message)
	case KindRegionViolation:
		return status.Error(codes.FailedPrecondition, e.Message)
	case KindUnavailable:
		return status.Error(codes.Unavailable, e.Message)
	case KindPayloadTooLarge:
		// ResourceExhausted is shared with rate-limiting; the proxy
		// disambiguates by reading the embedded type from the wire payload.
		return status.Error(codes.ResourceExhausted, e.EncodeWire())
	case KindMIMEMismatch:
		// FailedPrecondition is shared with other 4xx-ish outcomes; the
		// wire payload carries the specific type so the proxy can emit
		// 409 + MIME_MISMATCH with the declared/detected MIME details.
		return status.Error(codes.FailedPrecondition, e.EncodeWire())
	default:
		return status.Error(codes.Internal, e.Message)
	}
}

// ---- HTTP translation ------------------------------------------------------

// HTTPError is the canonical HTTP error body returned to clients.
type HTTPError struct {
	Code          int            `json:"-"`
	Type          string         `json:"type"`
	Message       string         `json:"message"`
	Field         string         `json:"field,omitempty"`
	Details       map[string]any `json:"details,omitempty"`
	CorrelationID string         `json:"correlation_id,omitempty"`
}

// ToHTTPError maps a domain error to an HTTPError. The caller is responsible
// for writing it with the returned Code.
func ToHTTPError(err error, correlationID string) HTTPError {
	if err == nil {
		return HTTPError{Code: http.StatusOK}
	}
	var e *Error
	if !errors.As(err, &e) {
		return HTTPError{
			Code:          http.StatusInternalServerError,
			Type:          "INTERNAL",
			Message:       "internal error",
			CorrelationID: correlationID,
		}
	}
	out := HTTPError{
		Type:          e.Code,
		Message:       e.Message,
		Field:         e.Field,
		Details:       e.Details,
		CorrelationID: correlationID,
	}
	switch e.Kind {
	case KindNotFound:
		out.Code = http.StatusNotFound
	case KindAlreadyExists, KindConflict:
		out.Code = http.StatusConflict
	case KindLegalHold:
		// Wave 8 P8.2 DoD: held documents return 423 Locked (not 409),
		// matching RFC 4918 semantics. gRPC translation stays
		// FailedPrecondition (same as CONFLICT bucket there).
		out.Code = http.StatusLocked
	case KindForbidden:
		out.Code = http.StatusForbidden
	case KindUnauthorized:
		out.Code = http.StatusUnauthorized
	case KindValidation:
		out.Code = http.StatusBadRequest
	case KindRegionViolation:
		// RFC 7725 — 451 Unavailable For Legal Reasons. Residency
		// rules ARE a legal-reasons surface; the frontend interceptor
		// disambiguates between geofence and region-pin causes via
		// the body's error_code.
		out.Code = http.StatusUnavailableForLegalReasons
	case KindRateLimited:
		out.Code = http.StatusTooManyRequests
	case KindUnavailable:
		out.Code = http.StatusServiceUnavailable
	case KindPayloadTooLarge:
		out.Code = http.StatusRequestEntityTooLarge
	case KindMIMEMismatch:
		out.Code = http.StatusConflict
	default:
		out.Code = http.StatusInternalServerError
		// Never leak internal detail to the wire.
		out.Message = "internal error"
	}
	return out
}
