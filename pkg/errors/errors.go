// Package errors defines VaultDMS domain errors and the translation rules
// between them and gRPC / HTTP wire errors. Handlers return domain errors;
// transport layers call ToGRPCError / ToHTTPError at the edge.
package errors

import (
	"errors"
	"fmt"
	"net/http"

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
	KindInternal
)

// Error is the base domain error. Callers construct concrete errors via the
// helpers below and wrap them with fmt.Errorf / errors.Join freely.
type Error struct {
	Kind    Kind
	Code    string
	Message string
	Field   string
	Cause   error
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Cause }

// Is implements errors.Is so wrapped sentinels still match. Wrap()
// copies the sentinel and sets Cause; the resulting pointer is not
// the same as the sentinel, so the default pointer-equality check
// in errors.Is fails. Two domain errors are "the same" when they
// share Kind + Code — the human-readable Message and the wrapped
// Cause are decorations, not identity.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	if !ok {
		return false
	}
	return e.Kind == t.Kind && e.Code == t.Code
}

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

// Wrap attaches a cause to a domain error, preserving its Kind and Code.
func Wrap(base *Error, cause error) *Error {
	if base == nil {
		return &Error{Kind: KindInternal, Code: "INTERNAL", Message: "internal error", Cause: cause}
	}
	cp := *base
	cp.Cause = cause
	return &cp
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
	default:
		return status.Error(codes.Internal, e.Message)
	}
}

// ---- HTTP translation ------------------------------------------------------

// HTTPError is the canonical HTTP error body returned to clients.
type HTTPError struct {
	Code          int       `json:"-"`
	Type          string    `json:"type"`
	Message       string    `json:"message"`
	Field         string    `json:"field,omitempty"`
	CorrelationID string    `json:"correlation_id,omitempty"`
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
	case KindValidation, KindRegionViolation:
		out.Code = http.StatusBadRequest
	case KindRateLimited:
		out.Code = http.StatusTooManyRequests
	default:
		out.Code = http.StatusInternalServerError
		// Never leak internal detail to the wire.
		out.Message = "internal error"
	}
	return out
}
