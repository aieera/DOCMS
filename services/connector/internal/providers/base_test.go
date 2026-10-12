package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// N-12: the token exchange/refresh (Google, M365) used http.DefaultClient —
// no timeout — on contexts that often carry no deadline.
func TestBaseOAuth_TokenClientHasTimeout(t *testing.T) {
	c := (&BaseOAuth{}).httpClient()
	if c == http.DefaultClient || c.Timeout <= 0 || c.Timeout > time.Minute {
		t.Fatalf("token client timeout = %v, want a bounded timeout", c.Timeout)
	}
}

// An oversized token response is not buffered whole.
func TestBaseOAuth_TokenResponseBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"access_token":"` + strings.Repeat("a", 4<<20) + `"}`))
	}))
	defer srv.Close()
	b := &BaseOAuth{TokenEndpoint: srv.URL}
	if _, err := b.ExchangeCode(context.Background(), "c", "https://r"); err == nil {
		t.Fatal("a 4 MiB token response was accepted")
	}
}
