package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/aieera/sedoc/pkg/auth"
)

// TestIdempotencyGating covers the no-DB decision paths: which requests pass
// through, which are forced to carry a key, and the key-length guard. The
// reserve/replay path needs Postgres and is exercised in the integration test.
func TestIdempotencyGating(t *testing.T) {
	apiKeyCtx := func(r *http.Request) *http.Request {
		return r.WithContext(auth.WithUser(r.Context(), auth.UserInfo{
			ID: uuid.New(), TenantID: uuid.New(), Role: apiKeyRole,
		}))
	}
	sessionCtx := func(r *http.Request) *http.Request {
		return r.WithContext(auth.WithUser(r.Context(), auth.UserInfo{
			ID: uuid.New(), TenantID: uuid.New(), Role: "member",
		}))
	}

	cases := []struct {
		name       string
		mw         func(*http.Request) *http.Request // ctx decorator
		required   bool
		method     string
		key        string
		wantStatus int
		wantNext   bool // handler should run (pass-through)
	}{
		{"optional, no key, post → passthrough", sessionCtx, false, http.MethodPost, "", 0, true},
		{"required, no key, api-key → 400", apiKeyCtx, true, http.MethodPost, "", http.StatusBadRequest, false},
		{"required, no key, session → passthrough", sessionCtx, true, http.MethodPost, "", 0, true},
		{"required, no key, api-key, GET → passthrough", apiKeyCtx, true, http.MethodGet, "", 0, true},
		{"key too long → 400", sessionCtx, false, http.MethodPost, longKey(201), http.StatusBadRequest, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ran bool
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				ran = true
				w.WriteHeader(http.StatusCreated)
			})
			var h http.Handler
			if tc.required {
				h = IdempotencyRequired(nil)(next)
			} else {
				h = Idempotency(nil)(next)
			}
			r := httptest.NewRequest(tc.method, "/api/v1/things", nil)
			if tc.key != "" {
				r.Header.Set("Idempotency-Key", tc.key)
			}
			r = tc.mw(r)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)

			if ran != tc.wantNext {
				t.Fatalf("handler ran = %v, want %v (status %d)", ran, tc.wantNext, w.Code)
			}
			if tc.wantStatus != 0 && w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, tc.wantStatus)
			}
		})
	}
}

func longKey(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}
