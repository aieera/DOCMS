// D-13 — deployment secrets fail closed outside dev.
//
// The zero-trust token pepper fell back to a constant in main.go and the
// OnlyOffice JWT secret to a published compose default, in every
// environment. Outside dev a missing secret — or the published dev value —
// must make the dependent routes answer 503 instead of running with a key
// anyone can read.
package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/services/document/internal/service"
)

func TestDeploymentSecrets_FailClosedOutsideDev(t *testing.T) {
	tenant, user, doc, ver := uuid.New(), uuid.New(), uuid.New(), uuid.New()

	t.Run("zt pepper", func(t *testing.T) {
		for _, env := range []string{"staging", "prod"} {
			for _, configured := range []string{"", DevZTTokenPepper} {
				pepper, err := service.ResolveSecret(env, configured, DevZTTokenPepper)
				if err == nil {
					t.Fatalf("env=%s configured=%q: pepper must be refused", env, configured)
				}
				// main hands the handler the (nil) result; nothing may run.
				admin, public := http.NewServeMux(), http.NewServeMux()
				zh := NewZTShareHandler(nil, nil, nil, nil, pepper)
				zh.RegisterAdmin(admin)
				zh.RegisterPublic(public)
				rec := httptest.NewRecorder()
				admin.ServeHTTP(rec, ztAsRole(httptest.NewRequest(http.MethodPost, "/api/v1/admin/share-links/zt",
					bytes.NewReader([]byte(`{}`))), tenant, user, "owner"))
				if rec.Code != http.StatusServiceUnavailable {
					t.Fatalf("zt create without pepper: want 503, got %d", rec.Code)
				}
				for _, path := range []string{"/manifest", "/stream"} {
					rec := httptest.NewRecorder()
					public.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/zt/"+uuid.NewString()+path, nil))
					if rec.Code != http.StatusServiceUnavailable {
						t.Fatalf("zt %s without pepper: want 503, got %d", path, rec.Code)
					}
				}
			}
		}
		if p, err := service.ResolveSecret("dev", "", DevZTTokenPepper); err != nil || string(p) != DevZTTokenPepper {
			t.Fatalf("dev keeps the convenience pepper: %q %v", p, err)
		}
	})

	t.Run("onlyoffice jwt", func(t *testing.T) {
		for _, configured := range []string{"", DevOnlyOfficeJWT} {
			t.Setenv("SEDOC_ONLYOFFICE_JWT", configured)
			oo := NewOnlyOfficeHandler(zerolog.Nop())
			t.Setenv("SEDOC_ENVIRONMENT", "prod")
			oo.Authz = &fakeCoauthAuthz{docID: doc, canWrite: true}
			mux := http.NewServeMux()
			oo.Register(mux)
			if rec := ooConfig(mux, tenant, user, doc, ver); rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("config with secret %q in prod: want 503, got %d", configured, rec.Code)
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
				"/api/v1/documents/"+doc.String()+"/versions/"+ver.String()+"/onlyoffice/callback",
				bytes.NewReader([]byte(`{"status":1,"key":"k"}`))))
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("callback with secret %q in prod: want 503, got %d", configured, rec.Code)
			}
		}
		// dev keeps working with the compose default.
		t.Setenv("SEDOC_ONLYOFFICE_JWT", DevOnlyOfficeJWT)
		oo := NewOnlyOfficeHandler(zerolog.Nop())
		t.Setenv("SEDOC_ENVIRONMENT", "dev")
		oo.Authz = &fakeCoauthAuthz{docID: doc, canWrite: true}
		mux := http.NewServeMux()
		oo.Register(mux)
		if rec := ooConfig(mux, tenant, user, doc, ver); rec.Code != http.StatusOK {
			t.Fatalf("dev config: want 200, got %d %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("irm signing key", func(t *testing.T) {
		t.Setenv("SEDOC_IRM_SIGNING_KEY", "")
		docSvc := service.New(nil, nil, nil, zerolog.Nop())
		docSvc.SetEnvironment("prod")
		h := NewIRMHandler(service.NewIRMService(nil, nil, nil, nil, docSvc, zerolog.Nop()), zerolog.Nop())
		mux := http.NewServeMux()
		h.Register(mux)
		body := `{"document_id":"` + doc.String() + `","recipients":[{"type":"email","ref":"x@example.com"}],"expires_at":"2099-01-01T00:00:00Z"}`
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, ztAsRole(httptest.NewRequest(http.MethodPost, "/api/v1/irm/export",
			bytes.NewReader([]byte(body))), tenant, user, "owner"))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("irm export without key in prod: want 503, got %d %s", rec.Code, rec.Body.String())
		}
	})
}
