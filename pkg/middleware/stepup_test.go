// ADR 0061 — step-up middleware error-path tests.
//
// We don't run the SQL path here (would need testcontainers
// Postgres + the migration applied); instead pin the
// header-/status-code contract so a refactor that softens the
// fail-closed behavior gets caught.
package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	vdmsauth "github.com/vaultdms/vaultdms/pkg/auth"
)

// stub handler that signals it ran.
func okHandler(t *testing.T) (http.Handler, *bool) {
	t.Helper()
	called := false
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	return h, &called
}

func TestRequireStepUp_DeniesWhenTenantMissing(t *testing.T) {
	h, called := okHandler(t)
	mw := RequireStepUp(nil, "", zerolog.Nop())(h)

	r := httptest.NewRequest("POST", "/sensitive", nil)
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
	if w.Header().Get(StepUpRequiredHeader) != StepUpRequiredHeaderValue {
		t.Errorf("missing %s header — client wouldn't know to show step-up modal",
			StepUpRequiredHeader)
	}
	if *called {
		t.Error("inner handler was called despite missing tenant — fail-closed broken")
	}
}

func TestRequireStepUp_DeniesWhenUserMissing(t *testing.T) {
	h, called := okHandler(t)
	mw := RequireStepUp(nil, "", zerolog.Nop())(h)

	// Tenant present but no user. Realistic only if a buggy
	// auth middleware sets one but not the other; the step-up
	// gate must still fail closed.
	ctx := vdmsauth.SetTenantID(httptest.NewRequest("POST", "/x", nil).Context(), uuid.New())
	r := httptest.NewRequest("POST", "/sensitive", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
	if *called {
		t.Error("inner handler ran without user identity")
	}
}

// Header constants pinned — clients depend on the exact string
// to decide whether to show the step-up modal vs redirect to login.
func TestRequireStepUp_HeaderConstantsStable(t *testing.T) {
	// Future refactor must not silently rename these without
	// coordinating with the frontend.
	if StepUpRequiredHeader != "X-Step-Up-Required" {
		t.Errorf("header name changed: %s", StepUpRequiredHeader)
	}
	if StepUpRequiredHeaderValue != "webauthn" {
		t.Errorf("header value changed: %s", StepUpRequiredHeaderValue)
	}
}
