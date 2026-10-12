package intake

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

const (
	testInternalKey   = "internal-key-for-tests"
	testGatewaySecret = "gateway-secret-for-tests"
)

// credentialServer asserts every request carries the credentials
// intelligence's /internal/v1/* routes require (C-8 / DEC-4).
func credentialServer(t *testing.T, wantTenant, respBody string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Internal-Service-Key"); got != testInternalKey {
			t.Errorf("%s: X-Internal-Service-Key = %q, want %q", r.URL.Path, got, testInternalKey)
		}
		if got := r.Header.Get("X-Gateway-Signature"); got != testGatewaySecret {
			t.Errorf("%s: X-Gateway-Signature = %q, want %q", r.URL.Path, got, testGatewaySecret)
		}
		if got := r.Header.Get("X-Auth-Tenant-ID"); got != wantTenant {
			t.Errorf("%s: X-Auth-Tenant-ID = %q, want %q", r.URL.Path, got, wantTenant)
		}
		_, _ = w.Write([]byte(respBody))
	}))
}

func TestAnalyze_SendsServiceCredentials(t *testing.T) {
	t.Setenv("SEDOC_GATEWAY_SECRET", testGatewaySecret)
	ts := credentialServer(t, "tenant-a", `{"segments":[]}`)
	defer ts.Close()
	svc := newTestCapture(ts.URL, nil)

	if _, err := svc.Analyze(context.Background(), "tenant-a", []byte(`{}`)); err != nil {
		t.Fatalf("analyze: %v", err)
	}
}

func TestCommit_SplitSendsServiceCredentials(t *testing.T) {
	t.Setenv("SEDOC_GATEWAY_SECRET", testGatewaySecret)
	ts := credentialServer(t, "t", `{"documents":[]}`)
	defer ts.Close()
	svc := newTestCapture(ts.URL, &fakeIngester{fail: map[int]bool{}})

	if _, err := svc.Commit(context.Background(), baseInput()); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// Without SEDOC_INTERNAL_API_KEY the call cannot authenticate: it must not
// be sent, and the error must name the missing setting.
func TestIntelligenceCall_InternalKeyMissingFailsClosed(t *testing.T) {
	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"documents":[]}`))
	}))
	defer ts.Close()
	svc := &CaptureService{intelBase: ts.URL, ingest: &fakeIngester{fail: map[int]bool{}},
		httpc: &http.Client{Timeout: 5 * time.Second}, log: zerolog.Nop()}

	if _, err := svc.Analyze(context.Background(), "t", []byte(`{}`)); err == nil ||
		!strings.Contains(err.Error(), "SEDOC_INTERNAL_API_KEY") {
		t.Fatalf("analyze: want SEDOC_INTERNAL_API_KEY error, got %v", err)
	}
	if _, err := svc.Commit(context.Background(), baseInput()); err == nil ||
		!strings.Contains(err.Error(), "SEDOC_INTERNAL_API_KEY") {
		t.Fatalf("commit: want SEDOC_INTERNAL_API_KEY error, got %v", err)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("no request may be sent without the internal key; got %d", n)
	}
}

func TestNewCaptureService_ReadsInternalKey(t *testing.T) {
	t.Setenv("SEDOC_INTERNAL_API_KEY", testInternalKey)
	if got := NewCaptureService(nil, zerolog.Nop()).internalKey; got != testInternalKey {
		t.Fatalf("internalKey = %q, want %q", got, testInternalKey)
	}
}
