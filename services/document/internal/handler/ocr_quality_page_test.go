package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/auth"
)

// The review route's {page} used to go through strconv.Atoi + int32(), so a
// value past MaxInt32 wrapped onto a different page (4294967297 -> 1) and
// reviewed it. It must be rejected as a validation error before the service
// is touched (svc is nil here: reaching it would panic).
func TestOCRQualityReview_RejectsPageOutsideInt32(t *testing.T) {
	h := NewOCRQualityHandler(nil, zerolog.Nop())
	mux := http.NewServeMux()
	h.Register(mux)

	for _, page := range []string{"4294967297", "2147483648", "0", "-1", "x"} {
		t.Run(page, func(t *testing.T) {
			url := "/api/v1/documents/" + uuid.NewString() + "/ocr-quality/" + uuid.NewString() + "/" + page + "/review"
			req := httptest.NewRequest(http.MethodPost, url, nil)
			req = req.WithContext(auth.WithUser(req.Context(), auth.UserInfo{
				ID: uuid.New(), TenantID: uuid.New(), Role: "admin",
			}))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("page %q: status = %d, want 400 (body %s)", page, rec.Code, rec.Body.String())
			}
		})
	}
}
