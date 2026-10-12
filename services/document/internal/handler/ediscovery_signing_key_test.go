// D-13 (post-merge) — the e-discovery manifest signing key fell back to
// SEDOC_GATEWAY_SECRET whenever SEDOC_EDISCOVERY_SIGNING_KEY was empty, so
// key separation never happened and a Secret holding an empty string
// silently reused the gateway secret. Unless SEDOC_ENVIRONMENT is
// explicitly dev, the key is required: the signing routes (sync export,
// export-job creation) answer 503 before any work.
package handler

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/services/document/internal/service"
)

func TestEDiscoverySigningKey_RequiredOutsideDev(t *testing.T) {
	t.Setenv("SEDOC_GATEWAY_SECRET", "gateway-secret")
	tenant, user := uuid.New(), uuid.New()

	for _, env := range []string{"<unset>", "staging", "prod"} {
		if env == "<unset>" {
			t.Setenv("SEDOC_ENVIRONMENT", "")
			_ = os.Unsetenv("SEDOC_ENVIRONMENT")
		} else {
			t.Setenv("SEDOC_ENVIRONMENT", env)
		}
		t.Setenv("SEDOC_EDISCOVERY_SIGNING_KEY", "")

		if k, err := service.EDiscoverySigningKey(); !errors.Is(err, service.ErrSecretNotConfigured) || k != "" {
			t.Fatalf("env=%s: key must be refused (no gateway-secret fallback), got %q %v", env, k, err)
		}

		// nil pool: reaching the database would panic, so a clean 503
		// proves the gate runs first.
		mux := http.NewServeMux()
		NewEDiscoveryHandler(service.New(nil, nil, nil, zerolog.Nop()), zerolog.Nop()).Register(mux)
		for _, req := range []struct{ path, body string }{
			{"/api/v1/admin/ediscovery/export", `{"case_id":"c1","document_ids":["` + uuid.NewString() + `"]}`},
			{"/api/v1/admin/ediscovery/jobs", `{"hold_id":"` + uuid.NewString() + `"}`},
		} {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, ztAsRole(httptest.NewRequest(http.MethodPost, req.path,
				bytes.NewReader([]byte(req.body))), tenant, user, "owner"))
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("env=%s %s: want 503, got %d %s", env, req.path, rec.Code, rec.Body.String())
			}
		}
	}

	// A dedicated key works everywhere.
	t.Setenv("SEDOC_ENVIRONMENT", "prod")
	t.Setenv("SEDOC_EDISCOVERY_SIGNING_KEY", "dedicated")
	if k, err := service.EDiscoverySigningKey(); err != nil || k != "dedicated" {
		t.Fatalf("configured key: %q %v", k, err)
	}
	// Explicit dev keeps the gateway-secret convenience fallback.
	t.Setenv("SEDOC_ENVIRONMENT", "dev")
	t.Setenv("SEDOC_EDISCOVERY_SIGNING_KEY", "")
	if k, err := service.EDiscoverySigningKey(); err != nil || k != "gateway-secret" {
		t.Fatalf("dev fallback: %q %v", k, err)
	}
}
