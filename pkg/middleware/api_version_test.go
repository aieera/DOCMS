package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAPIVersion_SetsHeaderOnEveryResponse(t *testing.T) {
	h := APIVersion(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/thing", nil))
	if got := rr.Header().Get("API-Version"); got != CurrentAPIVersion {
		t.Fatalf("API-Version = %q, want %q", got, CurrentAPIVersion)
	}
}

func TestDeprecate_AddsRFC9745Headers(t *testing.T) {
	future := time.Now().Add(180 * 24 * time.Hour).UTC()
	sunset := future.Add(180 * 24 * time.Hour)
	h := Deprecate(future, sunset, "https://docs/migrate")(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rr.Header().Get("Deprecation") == "" {
		t.Error("Deprecation header missing")
	}
	if rr.Header().Get("Sunset") == "" {
		t.Error("Sunset header missing")
	}
	if link := rr.Header().Get("Link"); link == "" || link[0] != '<' {
		t.Errorf("Link header malformed: %q", link)
	}
	if rr.Code != http.StatusOK {
		t.Errorf("before sunset — handler must run, got %d", rr.Code)
	}
}

func TestDeprecate_410AfterSunset(t *testing.T) {
	past := time.Now().Add(-1 * time.Hour).UTC()
	h := Deprecate(past, past, "https://docs/migrate")(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("underlying handler MUST NOT run past sunset")
		}),
	)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rr.Code != http.StatusGone {
		t.Fatalf("want 410, got %d", rr.Code)
	}
}

func TestDeprecate_EmptyLinkSkipsHeader(t *testing.T) {
	future := time.Now().Add(180 * 24 * time.Hour).UTC()
	h := Deprecate(future, future, "")(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}),
	)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rr.Header().Get("Link") != "" {
		t.Error("empty link → Link header should not be set")
	}
}
