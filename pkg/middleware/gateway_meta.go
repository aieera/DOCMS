package middleware

import (
	"net/http"

	"github.com/aieera/sedoc/pkg/auth"
)

// StampGatewayClientMeta copies the request-scoped client identity
// (client IP, user agent, correlation id) from ctx onto Grpc-Metadata-*
// headers so grpc-gateway forwards them across its loopback gRPC dial.
//
// QA SD-23: audit rows for document/folder events had an empty IP while
// auth events carried one. Every other link existed — CorrelationHTTP
// stamps ctx, the outbox insert persists ip_address, the publisher emits
// vdmsclientip, the audit consumer falls back to it — but Go ctx values
// do not cross grpc-gateway's client dial, and the per-service header
// injector forwarded tenant/user/role only. CorrelationInterceptor on
// the gRPC server side restores these keys into ctx.
func StampGatewayClientMeta(r *http.Request) {
	if ip := auth.GetClientIP(r.Context()); ip != "" {
		r.Header.Set("Grpc-Metadata-"+ClientIPMetadataKey, ip)
	}
	if ua := auth.GetUserAgent(r.Context()); ua != "" {
		r.Header.Set("Grpc-Metadata-"+UserAgentMetadataKey, ua)
	}
	if id := auth.GetCorrelationID(r.Context()); id != "" {
		r.Header.Set("Grpc-Metadata-"+CorrelationMetadataKey, id)
	}
}
