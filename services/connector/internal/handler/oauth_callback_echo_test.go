package handler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/services/connector/internal/service"
)

// TestOAuthCallbackError_NoInternalDetail: a failed connector OAuth callback
// used to answer 400 with err.Error(), which carried the vendor token
// exchange error, unseal failures or DB text. The body is now fixed; the
// detail goes to the error log with the request context. The two state
// sentinels keep their own (fixed, user-actionable) text.
func TestOAuthCallbackError_NoInternalDetail(t *testing.T) {
	var logBuf bytes.Buffer
	h := &Handler{log: zerolog.New(&logBuf)}
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/connectors/oauth/callback?code=c&state=s", nil)

	rec := httptest.NewRecorder()
	vendor := fmt.Errorf("google: exchange: %w", errors.New(`oauth2: "invalid_client" client_secret=GOCSPX-secret`))
	h.writeOAuthCallbackError(rec, r, "google", vendor)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "GOCSPX") || strings.Contains(body, "exchange") {
		t.Fatalf("body echoes internal detail: %s", body)
	}
	if !strings.Contains(logBuf.String(), "GOCSPX-secret") || !strings.Contains(logBuf.String(), `"level":"error"`) {
		t.Fatalf("detail must be logged at error; log = %s", logBuf.String())
	}

	for err, want := range map[error]int{
		service.ErrOAuthStateNotYours: http.StatusForbidden,
		service.ErrOAuthStateInvalid:  http.StatusBadRequest,
	} {
		rec := httptest.NewRecorder()
		h.writeOAuthCallbackError(rec, r, "google", err)
		if rec.Code != want || !strings.Contains(rec.Body.String(), err.Error()) {
			t.Fatalf("%v: got %d %s", err, rec.Code, rec.Body.String())
		}
	}
}
