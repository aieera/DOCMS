package vector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	testInternalKey   = "internal-key-for-tests"
	testGatewaySecret = "gateway-secret-for-tests"
)

// TestSemanticSearch_SendsServiceCredentials pins C-8 / DEC-4: the
// intelligence service's /internal/v1/* routes require the gateway
// signature, the internal service key and the tenant. Qdrant is a
// third-party store and must never see the internal key.
func TestSemanticSearch_SendsServiceCredentials(t *testing.T) {
	t.Setenv("SEDOC_GATEWAY_SECRET", testGatewaySecret)

	embedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Internal-Service-Key"); got != testInternalKey {
			t.Errorf("X-Internal-Service-Key = %q, want %q", got, testInternalKey)
		}
		if got := r.Header.Get("X-Gateway-Signature"); got != testGatewaySecret {
			t.Errorf("X-Gateway-Signature = %q, want %q", got, testGatewaySecret)
		}
		if got := r.Header.Get("X-Auth-Tenant-ID"); got != "tenant-1" {
			t.Errorf("X-Auth-Tenant-ID = %q, want tenant-1", got)
		}
		_ = json.NewEncoder(w).Encode(embedResp{Embedding: []float32{1}, Dimension: 1, Model: "m"})
	}))
	defer embedSrv.Close()

	qdrantSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Internal-Service-Key") != "" || r.Header.Get("X-Gateway-Signature") != "" {
			t.Error("service credentials leaked to Qdrant")
		}
		_, _ = w.Write([]byte(`{"result":[]}`))
	}))
	defer qdrantSrv.Close()

	c, err := New(Config{
		IntelligenceEmbedURL: embedSrv.URL,
		QdrantBaseURL:        qdrantSrv.URL,
		Collection:           "c",
		InternalKey:          testInternalKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.SemanticSearch(context.Background(), "q", "tenant-1", nil, 5); err != nil {
		t.Fatal(err)
	}
}

// TestSemanticSearch_InternalKeyMissingFailsClosed: without
// SEDOC_INTERNAL_API_KEY the call cannot authenticate, so it is not sent
// at all and the caller gets a clear error (hybrid falls back to lexical).
func TestSemanticSearch_InternalKeyMissingFailsClosed(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(embedResp{Embedding: []float32{1}, Dimension: 1, Model: "m"})
	}))
	defer srv.Close()

	c, err := New(Config{IntelligenceEmbedURL: srv.URL, QdrantBaseURL: srv.URL, Collection: "c"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.SemanticSearch(context.Background(), "q", "tenant-1", nil, 5)
	if err == nil || !strings.Contains(err.Error(), "SEDOC_INTERNAL_API_KEY") {
		t.Fatalf("want SEDOC_INTERNAL_API_KEY error, got %v", err)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("no request may be sent without the internal key; got %d", n)
	}
}
