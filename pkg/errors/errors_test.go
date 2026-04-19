package errors

import (
	"errors"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestConstructors(t *testing.T) {
	tests := []struct {
		name    string
		err     *Error
		kind    Kind
		code    string
		message string
		field   string
	}{
		{"validation", Validation("email", "required"), KindValidation, "INVALID_ARGUMENT", "required", "email"},
		{"validation no field", Validation("", "bad input"), KindValidation, "INVALID_ARGUMENT", "bad input", ""},
		{"not found", NotFound("doc not found"), KindNotFound, "NOT_FOUND", "doc not found", ""},
		{"conflict", Conflict("already processed"), KindConflict, "CONFLICT", "already processed", ""},
		{"forbidden", Forbidden("no access"), KindForbidden, "FORBIDDEN", "no access", ""},
		{"unauthorized", Unauthorized("invalid token"), KindUnauthorized, "UNAUTHORIZED", "invalid token", ""},
		{"internal", Internal("db crashed"), KindInternal, "INTERNAL", "db crashed", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err.Kind != tt.kind {
				t.Errorf("Kind: got %v, want %v", tt.err.Kind, tt.kind)
			}
			if tt.err.Code != tt.code {
				t.Errorf("Code: got %q, want %q", tt.err.Code, tt.code)
			}
			if tt.err.Message != tt.message {
				t.Errorf("Message: got %q, want %q", tt.err.Message, tt.message)
			}
			if tt.err.Field != tt.field {
				t.Errorf("Field: got %q, want %q", tt.err.Field, tt.field)
			}
		})
	}
}

func TestToGRPCError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want codes.Code
	}{
		{"nil", nil, codes.OK},
		{"validation", Validation("x", "bad"), codes.InvalidArgument},
		{"not found", NotFound("missing"), codes.NotFound},
		{"already exists", ErrAlreadyExists, codes.AlreadyExists},
		{"conflict", Conflict("foo"), codes.FailedPrecondition},
		{"legal hold", ErrLegalHold, codes.FailedPrecondition},
		{"region violation", ErrRegionViolation, codes.FailedPrecondition},
		{"forbidden", Forbidden("denied"), codes.PermissionDenied},
		{"unauthorized", Unauthorized("who"), codes.Unauthenticated},
		{"rate limited", ErrRateLimited, codes.ResourceExhausted},
		{"internal typed", Internal("boom"), codes.Internal},
		{"internal plain", errors.New("plain"), codes.Internal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToGRPCError(tt.err)
			if tt.err == nil {
				if got != nil {
					t.Fatalf("nil err: got %v, want nil", got)
				}
				return
			}
			st, ok := status.FromError(got)
			if !ok {
				t.Fatalf("not a status error: %v", got)
			}
			if st.Code() != tt.want {
				t.Errorf("code: got %v, want %v", st.Code(), tt.want)
			}
		})
	}
}

func TestToHTTPError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
		wantType string
	}{
		{"nil", nil, http.StatusOK, ""},
		{"validation", Validation("x", "bad"), http.StatusBadRequest, "INVALID_ARGUMENT"},
		{"not found", NotFound("missing"), http.StatusNotFound, "NOT_FOUND"},
		{"already exists", ErrAlreadyExists, http.StatusConflict, "ALREADY_EXISTS"},
		{"conflict", Conflict("foo"), http.StatusConflict, "CONFLICT"},
		{"legal hold", ErrLegalHold, http.StatusLocked, "LEGAL_HOLD"},
		{"forbidden", Forbidden("denied"), http.StatusForbidden, "FORBIDDEN"},
		{"unauthorized", Unauthorized("who"), http.StatusUnauthorized, "UNAUTHORIZED"},
		{"rate limited", ErrRateLimited, http.StatusTooManyRequests, "RATE_LIMITED"},
		{"internal typed", Internal("boom"), http.StatusInternalServerError, "INTERNAL"},
		{"internal plain", errors.New("plain"), http.StatusInternalServerError, "INTERNAL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToHTTPError(tt.err, "corr-1")
			if got.Code != tt.wantCode {
				t.Errorf("Code: got %d, want %d", got.Code, tt.wantCode)
			}
			if tt.wantType != "" && got.Type != tt.wantType {
				t.Errorf("Type: got %q, want %q", got.Type, tt.wantType)
			}
		})
	}
}

func TestToHTTPErrorScrubsInternalMessage(t *testing.T) {
	got := ToHTTPError(Internal("database password = hunter2"), "")
	if got.Message != "internal error" {
		t.Errorf("internal message must be scrubbed; got %q", got.Message)
	}
}

func TestWrapPreservesIsChain(t *testing.T) {
	cause := errors.New("underlying io failure")
	wrapped := Wrap(NotFound("doc"), cause)

	if !errors.Is(wrapped, cause) {
		t.Error("errors.Is should find wrapped cause")
	}
	if KindOf(wrapped) != KindNotFound {
		t.Errorf("KindOf: got %v, want %v", KindOf(wrapped), KindNotFound)
	}

	// Double-wrapped via fmt.Errorf %w still reachable.
	doubleWrapped := Wrap(Validation("f", "m"), cause)
	if !errors.Is(doubleWrapped, cause) {
		t.Error("errors.Is should find cause through Wrap")
	}

	// Nil base falls back to internal.
	nb := Wrap(nil, cause)
	if nb.Kind != KindInternal {
		t.Errorf("nil base Wrap: got Kind %v, want %v", nb.Kind, KindInternal)
	}
	if !errors.Is(nb, cause) {
		t.Error("errors.Is should find cause in nil-base Wrap")
	}
}

func TestKindOf(t *testing.T) {
	if KindOf(nil) != KindUnknown {
		t.Error("nil should be KindUnknown")
	}
	if KindOf(errors.New("plain")) != KindUnknown {
		t.Error("plain error should be KindUnknown")
	}
	if KindOf(Validation("x", "y")) != KindValidation {
		t.Error("Validation should be KindValidation")
	}
	if KindOf(Wrap(NotFound("x"), errors.New("cause"))) != KindNotFound {
		t.Error("wrapped NotFound should report KindNotFound")
	}
}

func TestFromPgError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantKind Kind
	}{
		{"nil", nil, KindUnknown},
		{"no rows", pgx.ErrNoRows, KindNotFound},
		{"unique violation", &pgconn.PgError{Code: "23505", Message: "duplicate"}, KindAlreadyExists},
		{"fk violation", &pgconn.PgError{Code: "23503", Message: "fk"}, KindConflict},
		{"check violation", &pgconn.PgError{Code: "23514", Message: "check"}, KindValidation},
		{"not null violation", &pgconn.PgError{Code: "23502", ColumnName: "email"}, KindValidation},
		{"unmapped pg error", &pgconn.PgError{Code: "42601", Message: "syntax"}, KindInternal},
		{"plain error", errors.New("other"), KindInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FromPgError(tt.err)
			if tt.err == nil {
				if got != nil {
					t.Fatalf("nil input: got %v, want nil", got)
				}
				return
			}
			if k := KindOf(got); k != tt.wantKind {
				t.Errorf("Kind: got %v, want %v", k, tt.wantKind)
			}
			// The original pg error must remain reachable via errors.Is.
			if !errors.Is(got, tt.err) {
				t.Error("original pg error should be reachable via errors.Is")
			}
		})
	}
}

func TestErrorString(t *testing.T) {
	// Base error string format.
	e := Validation("email", "required")
	if e.Error() != "INVALID_ARGUMENT: required" {
		t.Errorf("got %q", e.Error())
	}
	// Wrapped includes the cause.
	w := Wrap(NotFound("gone"), errors.New("db gone"))
	if got := w.Error(); got != "NOT_FOUND: gone: db gone" {
		t.Errorf("got %q", got)
	}
}
