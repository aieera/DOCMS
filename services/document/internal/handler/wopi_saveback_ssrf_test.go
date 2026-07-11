// SSRF posture of the OnlyOffice save-back download (SaveFromURL).
// These run without containers: every case fails BEFORE the save core
// touches a dependency.
package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func ssrfClaims() *WOPIClaims { return &WOPIClaims{CanWrite: true} }

func TestSaveFromURL_NoConfiguredHost_FailsClosed(t *testing.T) {
	res := NewDBWOPIResolver(nil, nil, nil, nil, nil, zerolog.Nop())
	err := res.SaveFromURL(context.Background(), ssrfClaims(),
		"http://anywhere.internal/f.docx", "" /* no DS configured */, "s")
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("want fail-closed error on empty allowedHost, got %v", err)
	}
}

func TestSaveFromURL_RedirectRefused(t *testing.T) {
	// An ALLOWED host that redirects elsewhere must not be followed —
	// that would turn the allow-list into an open proxy.
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	t.Cleanup(redirecting.Close)
	u, _ := url.Parse(redirecting.URL)

	res := NewDBWOPIResolver(nil, nil, nil, nil, nil, zerolog.Nop())
	err := res.SaveFromURL(context.Background(), ssrfClaims(),
		redirecting.URL+"/cache/f.docx", u.Host, "s")
	if err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("want redirect refusal, got %v", err)
	}
}

func TestSaveFromURL_SchemeAndHostPinned(t *testing.T) {
	res := NewDBWOPIResolver(nil, nil, nil, nil, nil, zerolog.Nop())
	if err := res.SaveFromURL(context.Background(), ssrfClaims(),
		"file:///etc/passwd", "ds.internal:80", "s"); err == nil {
		t.Fatal("file:// scheme must be refused")
	}
	if err := res.SaveFromURL(context.Background(), ssrfClaims(),
		"http://evil.example/f.docx", "ds.internal:80", "s"); err == nil ||
		!strings.Contains(err.Error(), "does not match") {
		t.Fatal("foreign host must be refused")
	}
}
