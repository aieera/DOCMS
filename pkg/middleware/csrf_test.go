package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// helper: build a request + response recorder wired through the
// CSRF middleware to a noop OK handler.
func runCSRF(t *testing.T, method string, mutate func(r *http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	h := CSRFDoubleSubmit()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	r := httptest.NewRequest(method, "/x", nil)
	if mutate != nil {
		mutate(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestCSRF_SafeMethodsBypass(t *testing.T) {
	for _, m := range []string{"GET", "HEAD", "OPTIONS"} {
		w := runCSRF(t, m, nil)
		if w.Code != http.StatusOK {
			t.Errorf("%s with no CSRF must pass; got %d", m, w.Code)
		}
	}
}

func TestCSRF_MissingCookieRejects(t *testing.T) {
	w := runCSRF(t, "POST", func(r *http.Request) {
		r.Header.Set(CSRFHeaderName, "anything")
	})
	if w.Code != http.StatusForbidden {
		t.Errorf("missing cookie must 403; got %d", w.Code)
	}
}

func TestCSRF_MissingHeaderRejects(t *testing.T) {
	w := runCSRF(t, "POST", func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: "abc"})
	})
	if w.Code != http.StatusForbidden {
		t.Errorf("missing header must 403; got %d", w.Code)
	}
}

func TestCSRF_MismatchRejects(t *testing.T) {
	w := runCSRF(t, "POST", func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: "cookie-val"})
		r.Header.Set(CSRFHeaderName, "different-val")
	})
	if w.Code != http.StatusForbidden {
		t.Errorf("mismatch must 403; got %d", w.Code)
	}
}

func TestCSRF_MatchPasses(t *testing.T) {
	w := runCSRF(t, "POST", func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: "same-val"})
		r.Header.Set(CSRFHeaderName, "same-val")
	})
	if w.Code != http.StatusOK {
		t.Errorf("match must pass; got %d body=%s", w.Code, w.Body.String())
	}
}

func TestCSRF_EmptyCookieRejects(t *testing.T) {
	// An empty cookie value + empty header would compare equal — but
	// the middleware must still reject it. Otherwise any request
	// without cookies would pass.
	w := runCSRF(t, "POST", func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: ""})
		r.Header.Set(CSRFHeaderName, "")
	})
	if w.Code != http.StatusForbidden {
		t.Errorf("empty cookie must 403 even if header also empty; got %d", w.Code)
	}
}

func TestCSRF_APIKeyBearerBypasses(t *testing.T) {
	// Bearer vdms_* = API key caller; never reaches the session
	// cookie + CSRF layer.
	w := runCSRF(t, "POST", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer vdms_testkey")
	})
	if w.Code != http.StatusOK {
		t.Errorf("api-key bearer must bypass CSRF; got %d", w.Code)
	}
}

func TestCSRF_SessionBearerDoesNotBypass(t *testing.T) {
	// Bearer session (non-vdms_) does NOT bypass — spec allows 30-day
	// back-compat for clients, but CSRF protection still applies to
	// session-cookie-paired mutations.
	w := runCSRF(t, "POST", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer randomsessiontoken")
	})
	if w.Code != http.StatusForbidden {
		t.Errorf("non-api-key bearer must NOT bypass CSRF; got %d", w.Code)
	}
}

func TestCSRF_AllMutatingMethodsEnforce(t *testing.T) {
	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		// No cookie / header should 403 on every mutating method.
		if w := runCSRF(t, m, nil); w.Code != http.StatusForbidden {
			t.Errorf("%s must 403 without CSRF; got %d", m, w.Code)
		}
	}
}
