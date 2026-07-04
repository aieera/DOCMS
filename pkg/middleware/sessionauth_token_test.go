package middleware

// ADR 0117 — Bearer-session extraction for cookie-less clients (mobile,
// add-ins). The DB-validation halves of SessionAuth are covered by the
// service integration suites; this pins the token-source precedence rules.

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func tokenReq(mutate func(r *http.Request)) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	if mutate != nil {
		mutate(r)
	}
	return r
}

func TestSessionTokenFromRequest(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(r *http.Request)
		want   string
	}{
		{"nothing", nil, ""},
		{"cookie only", func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: "dms_session", Value: "cook"})
		}, "cook"},
		{"bearer only", func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer sess-token")
		}, "sess-token"},
		{"cookie wins over bearer", func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: "dms_session", Value: "cook"})
			r.Header.Set("Authorization", "Bearer sess-token")
		}, "cook"},
		{"vdms_ bearer is an API key, not a session", func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer vdms_apikey123")
		}, ""},
		{"empty bearer", func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer ")
		}, ""},
		{"whitespace bearer", func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer    ")
		}, ""},
		{"non-bearer scheme", func(r *http.Request) {
			r.Header.Set("Authorization", "Basic dXNlcjpwdw==")
		}, ""},
		{"empty cookie falls through to bearer", func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: "dms_session", Value: ""})
			r.Header.Set("Authorization", "Bearer sess-token")
		}, "sess-token"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sessionTokenFromRequest(tokenReq(c.mutate), "dms_session"); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestSessionTokenFromRequest_CustomCookieName(t *testing.T) {
	r := tokenReq(func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: "other_session", Value: "v"})
	})
	if got := sessionTokenFromRequest(r, "other_session"); got != "v" {
		t.Fatalf("custom cookie name: got %q", got)
	}
	if got := sessionTokenFromRequest(r, "dms_session"); got != "" {
		t.Fatalf("wrong cookie name must miss: got %q", got)
	}
}
