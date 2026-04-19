package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSearchEndpoint_RequiresHeaders(t *testing.T) {
	mux := http.NewServeMux()
	// We can't easily wire a full service here (needs OpenSearch), so we
	// test only the header-validation layer. A nil service will panic after
	// validation — we expect a 400 before that.
	h := &Handler{} // svc is nil intentionally
	mux.HandleFunc("POST /api/v1/search", h.search)

	body := `{"query":"test"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/search", bytes.NewBufferString(body))
	// No X-Tenant-ID or X-User-ID headers.
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	var resp map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["error"] == "" {
		t.Fatal("expected error message in response")
	}
}

func TestAutocompleteEndpoint_RequiresQ(t *testing.T) {
	mux := http.NewServeMux()
	h := &Handler{}
	mux.HandleFunc("GET /api/v1/search/autocomplete", h.autocomplete)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/search/autocomplete", nil)
	req.Header.Set("X-Tenant-ID", "t1")
	req.Header.Set("X-User-ID", "u1")
	// Missing ?q= parameter.
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestSavedSearchEndpoint_ValidatesBody(t *testing.T) {
	mux := http.NewServeMux()
	h := &Handler{}
	mux.HandleFunc("POST /api/v1/saved-searches", h.createSavedSearch)

	// Empty name and query.
	body := `{"name":"","query":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/saved-searches", bytes.NewBufferString(body))
	req.Header.Set("X-Tenant-ID", "t1")
	req.Header.Set("X-User-ID", "u1")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestDeleteSavedSearch_RequiresID(t *testing.T) {
	mux := http.NewServeMux()
	h := &Handler{}
	mux.HandleFunc("DELETE /api/v1/saved-searches/", h.deleteSavedSearch)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/saved-searches/", nil)
	req.Header.Set("X-Tenant-ID", "t1")
	req.Header.Set("X-User-ID", "u1")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestSplitHeader(t *testing.T) {
	cases := []struct {
		in  string
		out []string
	}{
		{"", nil},
		{"a,b,c", []string{"a", "b", "c"}},
		{" a , b ", []string{"a", "b"}},
	}
	for _, tc := range cases {
		got := splitHeader(tc.in)
		if len(got) != len(tc.out) {
			t.Errorf("splitHeader(%q): got %v, want %v", tc.in, got, tc.out)
		}
	}
}
