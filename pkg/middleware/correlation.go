// Package middleware holds HTTP and gRPC middleware used by every SeDoc
// service. Each middleware is provided in both transport flavors; they share
// the same context keys defined in pkg/logger.
package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// CorrelationHeader is the HTTP header carrying the correlation ID.
const CorrelationHeader = "X-Correlation-ID"

// CorrelationMetadataKey is the gRPC metadata key carrying the correlation ID.
const CorrelationMetadataKey = "x-correlation-id"

// ClientIPMetadataKey / UserAgentMetadataKey carry the originating
// caller's IP and User-Agent across a gRPC hop. The HTTP edge stamps
// them on ctx (CorrelationHTTP); the client interceptor copies them
// into outgoing metadata; the server interceptor restores them onto the
// downstream ctx so an outbox write in a gRPC-invoked handler inherits
// the same audit context a direct REST write would.
const (
	ClientIPMetadataKey  = "x-client-ip"
	UserAgentMetadataKey = "x-user-agent"
	maxUserAgentChars    = 512
)

// CorrelationHTTP extracts or generates a UUIDv7 correlation ID, stores it
// on the context, and echoes it in the response header.
//
// Also stamps the client IP onto ctx (auth.WithClientIP) so any
// downstream layer that writes audit-relevant rows — currently the
// outbox repository, see services/document/internal/repository/
// misc_repo.go — can persist it without each service having to add a
// separate middleware. Sourced from X-Forwarded-For (first IP) then
// X-Real-IP then RemoteAddr; empty when none are populated.
func CorrelationHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(CorrelationHeader)
		if id == "" {
			id = newID()
		}
		w.Header().Set(CorrelationHeader, id)
		ctx := auth.SetCorrelationID(r.Context(), id)
		if ip := clientIPFromRequest(r); ip != "" {
			ctx = auth.WithClientIP(ctx, ip)
		}
		// User-Agent rides the same path as the client IP so audit
		// rows can attribute an action to a device/browser. Capped so a
		// hostile or malformed header can't bloat the outbox row /
		// audit_events.user_agent column.
		if ua := truncateUserAgent(r.UserAgent()); ua != "" {
			ctx = auth.WithUserAgent(ctx, ua)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// clientIPFromRequest resolves the caller's IP from standard
// reverse-proxy headers, falling back to RemoteAddr. The first
// comma-separated entry in X-Forwarded-For wins (the originating
// client; subsequent entries are proxy hops).
//
// Mirrors pkg/middleware/requestlog.go's clientIP() but lives here so
// it can be used outside the request-logging path.
func clientIPFromRequest(r *http.Request) string {
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		if i := strings.IndexByte(v, ','); i > 0 {
			return strings.TrimSpace(v[:i])
		}
		return strings.TrimSpace(v)
	}
	if v := r.Header.Get("X-Real-IP"); v != "" {
		return strings.TrimSpace(v)
	}
	host := r.RemoteAddr
	if i := strings.LastIndexByte(host, ':'); i > 0 {
		host = host[:i]
	}
	return host
}

// CorrelationInterceptor is the gRPC equivalent of CorrelationHTTP. In
// addition to the correlation ID, it restores the originating client IP
// and User-Agent from incoming metadata (set by ClientPropagationInterceptor
// upstream) so audit context survives a service-to-service hop.
func CorrelationInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		id := ""
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if vals := md.Get(CorrelationMetadataKey); len(vals) > 0 {
				id = vals[0]
			}
			if vals := md.Get(ClientIPMetadataKey); len(vals) > 0 && vals[0] != "" {
				ctx = auth.WithClientIP(ctx, vals[0])
			}
			if vals := md.Get(UserAgentMetadataKey); len(vals) > 0 {
				if ua := truncateUserAgent(vals[0]); ua != "" {
					ctx = auth.WithUserAgent(ctx, ua)
				}
			}
		}
		if id == "" {
			id = newID()
		}
		ctx = auth.SetCorrelationID(ctx, id)
		_ = grpc.SetHeader(ctx, metadata.Pairs(CorrelationMetadataKey, id))
		return handler(ctx, req)
	}
}

// ClientPropagationInterceptor is a unary client interceptor that copies
// the correlation ID, client IP, and User-Agent from the calling ctx
// into outgoing gRPC metadata. Attach it to upstream gRPC clients (e.g.
// the graphql-gateway dials) so a downstream handler's audit/outbox
// writes carry the same caller attribution the HTTP edge captured.
func ClientPropagationInterceptor() grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		var kv []string
		if id := auth.GetCorrelationID(ctx); id != "" {
			kv = append(kv, CorrelationMetadataKey, id)
		}
		if ip := auth.GetClientIP(ctx); ip != "" {
			kv = append(kv, ClientIPMetadataKey, ip)
		}
		if ua := auth.GetUserAgent(ctx); ua != "" {
			kv = append(kv, UserAgentMetadataKey, ua)
		}
		if len(kv) > 0 {
			ctx = metadata.AppendToOutgoingContext(ctx, kv...)
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// truncateUserAgent bounds a User-Agent string to maxUserAgentChars so a
// hostile or malformed header can't bloat outbox rows or audit storage.
func truncateUserAgent(ua string) string {
	ua = strings.TrimSpace(ua)
	if len(ua) > maxUserAgentChars {
		return ua[:maxUserAgentChars]
	}
	return ua
}

func newID() string {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.NewString()
	}
	return id.String()
}
