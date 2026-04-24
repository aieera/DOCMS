package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/pkg/trustedproxy"
)

func newPlatformMux(t *testing.T) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	h := &Handler{svc: nil, log: zerolog.Nop()}
	h.RegisterPlatform(mux)
	return mux
}

func TestPlatform_MetricsQuery_RequiresAdminRole(t *testing.T) {
	mux := newPlatformMux(t)
	req := httptest.NewRequest("POST", "/api/v1/platform/metrics/query",
		bytes.NewBufferString(`{"query":"up"}`))
	req.Header.Set("X-User-Role", "member")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403 for non-admin, got %d", w.Code)
	}
}

func TestPlatform_MetricsQuery_503WhenPromUnset(t *testing.T) {
	t.Setenv(EnvPrometheusURL, "")
	mux := newPlatformMux(t)
	req := httptest.NewRequest("POST", "/api/v1/platform/metrics/query",
		bytes.NewBufferString(`{"query":"up"}`))
	req.Header.Set("X-User-Role", "admin")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 when prometheus unconfigured, got %d", w.Code)
	}
}

func TestPlatform_MetricsQuery_ForwardsToUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query" {
			t.Errorf("want /api/v1/query, got %s", r.URL.Path)
		}
		if r.URL.Query().Get("query") != "up" {
			t.Errorf("query not forwarded: %s", r.URL.Query())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer upstream.Close()

	t.Setenv(EnvPrometheusURL, upstream.URL)
	mux := newPlatformMux(t)
	req := httptest.NewRequest("POST", "/api/v1/platform/metrics/query",
		bytes.NewBufferString(`{"query":"up"}`))
	req.Header.Set("X-User-Role", "admin")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"resultType":"vector"`) {
		t.Fatalf("upstream payload not forwarded: %s", w.Body.String())
	}
}

func TestPlatform_MetricsQuery_MissingQuery400(t *testing.T) {
	t.Setenv(EnvPrometheusURL, "http://prometheus:9090")
	mux := newPlatformMux(t)
	req := httptest.NewRequest("POST", "/api/v1/platform/metrics/query",
		bytes.NewBufferString(`{"query":""}`))
	req.Header.Set("X-User-Role", "admin")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestPlatform_TrustedProxy_ResolvesClient(t *testing.T) {
	// Configure the process-wide default so the handler sees the
	// same CIDR list the real middleware would.
	cfg, err := trustedproxy.Parse("10.0.0.0/8")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	trustedproxy.SetDefault(cfg)
	t.Cleanup(func() { trustedproxy.SetDefault(trustedproxy.Config{}) })

	mux := newPlatformMux(t)
	body := `{"remote_addr":"10.0.0.5:80","x_forwarded_for":"198.51.100.9, 10.0.0.4"}`
	req := httptest.NewRequest("POST", "/api/v1/platform/trusted-proxy/test",
		bytes.NewBufferString(body))
	req.Header.Set("X-User-Role", "admin")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", w.Code, w.Body.String())
	}

	var out trustedProxyTestResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.ResolvedIP != "198.51.100.9" {
		t.Fatalf("want resolved 198.51.100.9, got %q", out.ResolvedIP)
	}
	if !out.Peer.Trusted {
		t.Fatal("peer 10.0.0.5 must be marked trusted")
	}
	if len(out.Hops) != 2 {
		t.Fatalf("want 2 hop results, got %d", len(out.Hops))
	}
	if out.Hops[0].Addr != "198.51.100.9" || out.Hops[0].Trusted {
		t.Fatalf("hop[0]: want untrusted 198.51.100.9, got %+v", out.Hops[0])
	}
	if out.Hops[1].Addr != "10.0.0.4" || !out.Hops[1].Trusted {
		t.Fatalf("hop[1]: want trusted 10.0.0.4, got %+v", out.Hops[1])
	}
	if len(out.TrustedCIDRs) != 1 || out.TrustedCIDRs[0] != "10.0.0.0/8" {
		t.Fatalf("CIDR echo wrong: %v", out.TrustedCIDRs)
	}
}

func TestPlatform_TrustedProxy_BadInputIsNonFatal(t *testing.T) {
	trustedproxy.SetDefault(trustedproxy.Config{})
	mux := newPlatformMux(t)
	req := httptest.NewRequest("POST", "/api/v1/platform/trusted-proxy/test",
		bytes.NewBufferString(`{"remote_addr":"10.0.0.5:80","x_forwarded_for":"bogus, 10.0.0.4"}`))
	req.Header.Set("X-User-Role", "admin")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("bad XFF entry must not 500 — got %d", w.Code)
	}
	var out trustedProxyTestResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Hops[0].Parseable {
		t.Fatal("bogus hop must flag Parseable=false")
	}
}
