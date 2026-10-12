package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/services/signature/internal/service"
)

// TestEsignOAuthStart_ErrorNotEchoed: StartOAuth's non-forbidden failures
// (provider config, nonce, DB persist) used to reach the client verbatim
// in a 400. The body is now fixed and the detail is logged at error.
func TestEsignOAuthStart_ErrorNotEchoed(t *testing.T) {
	var logBuf bytes.Buffer
	h := &Handler{svc: &service.Service{}, log: zerolog.New(&logBuf)} // no e-sign configured
	mux := http.NewServeMux()
	h.RegisterESign(mux)

	r := httptest.NewRequest(http.MethodPost, "/api/v1/signatures/esign/oauth/start", bytes.NewBufferString(`{"provider":"docusign"}`))
	r = r.WithContext(auth.WithUser(r.Context(), auth.UserInfo{ID: uuid.New(), TenantID: uuid.New(), Role: "admin"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if strings.Contains(w.Body.String(), "esign not configured") {
		t.Fatalf("body echoes the service error: %s", w.Body.String())
	}
	if !strings.Contains(logBuf.String(), "esign not configured") || !strings.Contains(logBuf.String(), `"level":"error"`) {
		t.Fatalf("detail must be logged at error; log = %s", logBuf.String())
	}
}
