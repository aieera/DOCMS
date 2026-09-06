package middleware

import (
	"net/http/httptest"
	"testing"

	"github.com/aieera/sedoc/pkg/auth"
)

// QA SD-23: audit rows for document/folder events had no IP while auth
// events did. The whole chain existed (HTTP middleware → ctx → outbox
// column → CloudEvents vdmsclientip → audit fallback) EXCEPT the
// grpc-gateway hop: ctx values don't cross the loopback gRPC dial, and
// the gateway's header-injector forwarded tenant/user/role but not the
// client IP / user agent / correlation id. StampGatewayClientMeta closes
// that gap; CorrelationInterceptor on the server side restores them.
func TestStampGatewayClientMeta_ForwardsCtxIdentity(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/v1/documents/x", nil)
	ctx := auth.WithClientIP(r.Context(), "203.0.113.9")
	ctx = auth.WithUserAgent(ctx, "Chrome on Windows")
	ctx = auth.SetCorrelationID(ctx, "corr-123")
	r = r.WithContext(ctx)

	StampGatewayClientMeta(r)

	if got := r.Header.Get("Grpc-Metadata-X-Client-Ip"); got != "203.0.113.9" {
		t.Errorf("client ip header = %q", got)
	}
	if got := r.Header.Get("Grpc-Metadata-X-User-Agent"); got != "Chrome on Windows" {
		t.Errorf("user agent header = %q", got)
	}
	if got := r.Header.Get("Grpc-Metadata-X-Correlation-Id"); got != "corr-123" {
		t.Errorf("correlation header = %q", got)
	}
}

func TestStampGatewayClientMeta_NoCtxValuesNoHeaders(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	StampGatewayClientMeta(r)
	if r.Header.Get("Grpc-Metadata-X-Client-Ip") != "" {
		t.Error("must not invent a client ip")
	}
}
