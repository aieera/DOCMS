// D-13 follow-up — with SEDOC_ENVIRONMENT UNSET (pkg/config would report
// "dev"), every dev-default secret must be refused: the routes answer 503
// instead of running on the published constant.
package handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/services/document/internal/service"
)

func TestDevSecretFallbacks_UnsetEnvironment503(t *testing.T) {
	t.Setenv("SEDOC_ENVIRONMENT", "")
	if err := os.Unsetenv("SEDOC_ENVIRONMENT"); err != nil {
		t.Fatal(err)
	}
	tenant, user, doc, ver := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	post := func(mux *http.ServeMux, path, body string) int {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, ztAsRole(httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, bytes.NewReader([]byte(body))), tenant, user, "owner"))
		return rec.Code
	}

	t.Run("onlyoffice jwt", func(t *testing.T) {
		t.Setenv("SEDOC_ONLYOFFICE_JWT", DevOnlyOfficeJWT)
		oo := NewOnlyOfficeHandler(zerolog.Nop())
		oo.Authz = &fakeCoauthAuthz{docID: doc, canWrite: true}
		mux := http.NewServeMux()
		oo.Register(mux)
		if rec := ooConfig(mux, tenant, user, doc, ver); rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("config: want 503, got %d", rec.Code)
		}
	})

	t.Run("zt pepper", func(t *testing.T) {
		t.Setenv("SEDOC_ZT_TOKEN_PEPPER", DevZTTokenPepper)
		pepper, err := ResolveZTPepper()
		if err == nil || pepper != nil {
			t.Fatalf("pepper must be refused: %q %v", pepper, err)
		}
		mux := http.NewServeMux()
		NewZTShareHandler(nil, nil, nil, nil, pepper).RegisterAdmin(mux)
		if c := post(mux, "/api/v1/admin/share-links/zt", `{}`); c != http.StatusServiceUnavailable {
			t.Fatalf("zt create: want 503, got %d", c)
		}
	})

	t.Run("dsr salt", func(t *testing.T) {
		t.Setenv("SEDOC_DSR_ANONYMIZE_SALT", DevDSRSalt)
		salt, err := ResolveDSRSalt()
		if err == nil || salt != nil {
			t.Fatalf("salt must be refused: %q %v", salt, err)
		}
		mux := http.NewServeMux()
		NewPrivacyHandler(nil, nil, string(salt), zerolog.Nop()).Register(mux)
		if c := post(mux, "/api/v1/privacy/dsr/anonymize", `{"subject_email":"x@example.com"}`); c != http.StatusServiceUnavailable {
			t.Fatalf("dsr anonymise: want 503, got %d", c)
		}
	})

	t.Run("irm signing key", func(t *testing.T) {
		t.Setenv("SEDOC_IRM_SIGNING_KEY", "sedoc-dev-irm-signing-key-do-not-use-in-prod")
		docSvc := service.New(nil, nil, nil, zerolog.Nop())
		docSvc.SetEnvironment("dev") // pkg/config's default for an unset variable
		mux := http.NewServeMux()
		NewIRMHandler(service.NewIRMService(nil, nil, nil, nil, docSvc, zerolog.Nop()), zerolog.Nop()).Register(mux)
		body := `{"document_id":"` + doc.String() + `","recipients":[{"type":"email","ref":"x@example.com"}],"expires_at":"2099-01-01T00:00:00Z"}`
		if c := post(mux, "/api/v1/irm/export", body); c != http.StatusServiceUnavailable {
			t.Fatalf("irm export: want 503, got %d", c)
		}
	})
}
