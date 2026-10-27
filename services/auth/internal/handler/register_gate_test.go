package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// DEF-09 — public POST /api/v1/auth/register created an ACTIVE member in
// whichever tenant the body named. Accounts now come only from invitations,
// SSO/SCIM or admins; self-registration is a deployment flag
// (SEDOC_SELF_REGISTRATION_ENABLED), off by default. With the flag off the
// route stays mounted (and rate-limited) but answers 403 before reading the
// body or touching the service: the handler here has no Service at all, so
// any work past the gate would panic into a 500.
func TestRegister_DisabledByDefault_403BeforeAnyWork(t *testing.T) {
	h := New(Config{Logger: zerolog.Nop()})
	router := h.Router(nil, nil, nil, nil, nil, nil, nil, nil, nil)

	body := `{"email":"mallory@example.com","password":"Correct-Horse-9!","display_name":"M","tenant_slug":"acme"}`
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/auth/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, "FORBIDDEN", got["type"])
	require.Equal(t, "self-registration is disabled", got["message"])
}

// Malformed bodies get the same answer: the gate runs before the body is
// parsed, so a disabled deployment never reveals input-validation detail.
func TestRegister_Disabled_IgnoresBody(t *testing.T) {
	h := New(Config{Logger: zerolog.Nop(), SelfRegistrationEnabled: false})
	router := h.Router(nil, nil, nil, nil, nil, nil, nil, nil, nil)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/auth/register", strings.NewReader(`not json`))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
}
