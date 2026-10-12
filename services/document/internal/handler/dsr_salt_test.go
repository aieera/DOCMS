// D-13 (post-merge) — the DSR anonymisation salt fell back to the
// hard-coded "vaultdms-dev-salt" whenever SEDOC_DSR_ANONYMIZE_SALT was
// unset, so GDPR pseudonyms (HMAC(value, salt)) were identical and
// predictable across every production install. Outside dev the salt is now
// required: an unset or dev-default salt leaves it empty and the anonymise
// route answers 503 before recording or dispatching anything.
package handler

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/services/document/internal/service"
)

func TestDSRSalt_RequiredOutsideDev(t *testing.T) {
	for _, env := range []string{"staging", "prod", ""} {
		for _, configured := range []string{"", DevDSRSalt} {
			salt, err := service.ResolveSecret(env, configured, DevDSRSalt)
			if !errors.Is(err, service.ErrSecretNotConfigured) || salt != nil {
				t.Fatalf("env=%q salt=%q: want ErrSecretNotConfigured, got %q %v", env, configured, salt, err)
			}
			// main hands the handler the (empty) result: nil pool and nil
			// Temporal client prove nothing is touched before the 503.
			h := NewPrivacyHandler(nil, nil, string(salt), zerolog.Nop())
			mux := http.NewServeMux()
			h.Register(mux)
			req := ztAsRole(httptest.NewRequest(http.MethodPost, "/api/v1/privacy/dsr/anonymize",
				bytes.NewReader([]byte(`{"subject_email":"x@example.com"}`))), uuid.New(), uuid.New(), "owner")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("env=%q salt=%q: anonymise want 503, got %d %s", env, configured, rec.Code, rec.Body.String())
			}
		}
	}
	if s, err := service.ResolveSecret("dev", "", DevDSRSalt); err != nil || string(s) != DevDSRSalt {
		t.Fatalf("dev keeps the convenience salt: %q %v", s, err)
	}
	if s, err := service.ResolveSecret("prod", "real-salt", DevDSRSalt); err != nil || string(s) != "real-salt" {
		t.Fatalf("configured salt: %q %v", s, err)
	}
}
