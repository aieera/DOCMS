package workflows

// T-D-8 regression guard. scheduleErrIsBenign must take the typed
// errors.As branch — not any string-contents fallback — for Temporal's
// AlreadyExists. Breakage was the whole motivation for T-D-8: an SDK
// bump that changed the error text used to silently take a
// slower/wrong path.

import (
	"errors"
	"testing"

	"go.temporal.io/api/serviceerror"
)

func TestScheduleErrIsBenign_Nil(t *testing.T) {
	if !scheduleErrIsBenign(nil) {
		t.Fatal("nil must be benign")
	}
}

func TestScheduleErrIsBenign_TypedAlreadyExists(t *testing.T) {
	// serviceerror.NewAlreadyExists is the public constructor the
	// Temporal server emits. Wrapped so we also prove errors.As
	// traverses wraps (what bootstrap code actually sees).
	inner := serviceerror.NewAlreadyExist("schedule already exists")
	wrapped := wrap(inner)
	if !scheduleErrIsBenign(wrapped) {
		t.Fatalf("wrapped *AlreadyExists must be benign; got err=%v", wrapped)
	}
}

func TestScheduleErrIsBenign_OtherErrorsNotBenign(t *testing.T) {
	// A string-contents fallback would silently swallow these. With
	// the typed check this must return false — a real bootstrap
	// failure now surfaces instead of being treated as idempotent.
	cases := []struct {
		name string
		err  error
	}{
		{"plain string match", errors.New("schedule already exists in another region")},
		{"plain error", errors.New("context deadline exceeded")},
		{"not-found is different", serviceerror.NewNotFound("schedule does not exist")},
		{"internal is different", serviceerror.NewInternal("server exploded")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if scheduleErrIsBenign(c.err) {
				t.Fatalf("%q must NOT be benign — only *serviceerror.AlreadyExists should swallow", c.err)
			}
		})
	}
}

// wrap mirrors the pattern callers actually see: the SDK layers its
// own error around the server's typed one. A simple fmt.Errorf with
// %w is enough because errors.As walks the chain.
func wrap(err error) error {
	return wrappedErr{inner: err}
}

type wrappedErr struct{ inner error }

func (w wrappedErr) Error() string { return "bootstrap: " + w.inner.Error() }
func (w wrappedErr) Unwrap() error { return w.inner }
