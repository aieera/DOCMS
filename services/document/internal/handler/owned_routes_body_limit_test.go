// DEC-19 — request body limits on the DOC3-owned routes (zt share,
// OnlyOffice, coauth/start, watermark + admin, IRM + admin, db-info). None of them carries document
// bytes in the request (OnlyOffice save-back downloads by URL), so each is
// capped at the 1 MiB JSON limit; an over-limit request is 413 before the
// handler runs.
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

func TestOwnedRoutes_MaxBody(t *testing.T) {
	t.Setenv("SEDOC_ONLYOFFICE_JWT", ooTestSecret)
	tenant, user := uuid.New(), uuid.New()

	mux := http.NewServeMux()
	zh := NewZTShareHandler(nil, nil, nil, nil, []byte("pepper"))
	zh.RegisterAdmin(mux)
	zh.RegisterPublic(mux)
	NewOnlyOfficeHandler(zerolog.Nop()).Register(mux)
	docSvc := service.New(nil, nil, nil, zerolog.Nop())
	NewIRMHandler(service.NewIRMService(nil, nil, nil, nil, docSvc, zerolog.Nop()), zerolog.Nop()).Register(mux)
	NewWatermarkHandler(docSvc, nil, nil, nil, nil, zerolog.Nop()).Register(mux)
	NewDBInfoHandler(nil).Register(mux)
	NewWatermarkAdminHandler(docSvc, zerolog.Nop()).Register(mux)
	NewIRMAdminHandler(service.NewIRMService(nil, nil, nil, nil, docSvc, zerolog.Nop()), zerolog.Nop()).Register(mux)
	NewCoauthStartHandler(docSvc, zerolog.Nop()).Register(mux)

	big := bytes.Repeat([]byte("a"), (1<<20)+1)
	doc, ver := uuid.NewString(), uuid.NewString()
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/admin/share-links/zt"},
		{http.MethodPost, "/api/v1/zt/" + uuid.NewString() + "/telemetry"},
		{http.MethodPost, "/api/v1/documents/" + doc + "/versions/" + ver + "/onlyoffice/callback"},
		{http.MethodPost, "/api/v1/irm/export"},
		{http.MethodPost, "/api/v1/irm/licenses/check"},
		{http.MethodGet, "/api/v1/documents/" + doc + "/versions/" + ver + "/wm/status"},
		{http.MethodGet, "/api/v1/admin/platform/db-info"},
		{http.MethodPut, "/api/v1/admin/watermark/config"},
		{http.MethodPost, "/api/v1/admin/watermark/overrides"},
		{http.MethodPost, "/api/v1/admin/irm/licenses/" + uuid.NewString() + "/revoke"},
		{http.MethodPost, "/api/v1/documents/" + doc + "/versions/" + ver + "/coauth/start"},
	} {
		req := ztAsRole(httptest.NewRequest(route.method, route.path, bytes.NewReader(big)), tenant, user, "owner")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s %s with a %d-byte body: want 413, got %d", route.method, route.path, len(big), rec.Code)
		}
	}
}
