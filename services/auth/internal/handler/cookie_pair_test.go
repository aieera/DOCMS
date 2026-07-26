package handler

// Regression guards for two halves of the intermittent auto-logout bug:
//
//  1. The SAML/OIDC login paths set dms_session but forgot dms_csrf.
//     dms_csrf is the ONLY JS-visible session signal (dms_session is
//     HttpOnly), so an SSO user's frontend saw "no session" and force-
//     logged-out on every 401 — and every mutating request failed the
//     CSRF double-submit. issueSessionCookies is the single choke point
//     that always issues the pair; the source-scan test below keeps any
//     future login flow from bypassing it.
//
//  2. The cookie horizon was the session's INITIAL expiry (login+24h),
//     but the server slides the session up to SessionMaxLifetime. The
//     browser dropped the cookie at hour 24 regardless of activity —
//     a guaranteed mid-task logout. The cookie must survive the longest
//     possible server-side life; the server stays the authority on
//     whether the session is actually alive.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aieera/sedoc/services/auth/internal/service"
)

func cookieByName(t *testing.T, cookies []*http.Cookie, name string) *http.Cookie {
	t.Helper()
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("cookie %q not set; got %v", name, cookies)
	return nil
}

func TestIssueSessionCookies_AlwaysSetsThePair(t *testing.T) {
	h := &Handler{cookieName: "dms_session"}
	rec := httptest.NewRecorder()

	sessionExpiry := time.Now().Add(service.SessionTTL)
	h.issueSessionCookies(rec, "tok-123", sessionExpiry)

	cookies := rec.Result().Cookies()
	sess := cookieByName(t, cookies, "dms_session")
	csrf := cookieByName(t, cookies, CSRFCookieName)

	if sess.Value != "tok-123" {
		t.Fatalf("session cookie value = %q", sess.Value)
	}
	if !sess.HttpOnly {
		t.Fatal("dms_session must be HttpOnly")
	}
	if csrf.HttpOnly {
		t.Fatal("dms_csrf must be JS-readable (not HttpOnly)")
	}
	if csrf.Value == "" {
		t.Fatal("dms_csrf must carry a token")
	}

	// The cookie must outlive every possible sliding extension —
	// horizon is SessionMaxLifetime, not the initial session expiry.
	minHorizon := int((service.SessionMaxLifetime - time.Hour).Seconds())
	if sess.MaxAge < minHorizon {
		t.Fatalf("dms_session Max-Age %ds dies before the session's max lifetime (%v) — sliding extensions would be lost", sess.MaxAge, service.SessionMaxLifetime)
	}
	if csrf.MaxAge < minHorizon {
		t.Fatalf("dms_csrf Max-Age %ds must match its session cookie", csrf.MaxAge)
	}
}

// Every browser login flow must issue the session/CSRF pair via
// issueSessionCookies. A direct setSessionCookie call outside handler.go
// is exactly how the SAML/OIDC flows shipped without dms_csrf.
func TestNoDirectSessionCookieCallSitesOutsideHandlerGo(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var offenders []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") || name == "handler.go" {
			continue
		}
		src, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if strings.Contains(line, "setSessionCookie(") || strings.Contains(line, "setCSRFCookie(") {
				offenders = append(offenders, name+":"+strings.TrimSpace(line)+" (line "+itoa(i+1)+")")
			}
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("login flows must use issueSessionCookies (session+CSRF pair, shared horizon); direct calls found:\n%s",
			strings.Join(offenders, "\n"))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
