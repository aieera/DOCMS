// Package middleware holds HTTP and gRPC middleware used by every VaultDMS
// service. Each middleware is provided in both transport flavors; they share
// the same context keys defined in pkg/logger.
package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/vaultdms/vaultdms/pkg/auth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// CorrelationHeader is the HTTP header carrying the correlation ID.
const CorrelationHeader = "X-Correlation-ID"

// CorrelationMetadataKey is the gRPC metadata key carrying the correlation ID.
const CorrelationMetadataKey = "x-correlation-id"

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

// CorrelationInterceptor is the gRPC equivalent of CorrelationHTTP.
func CorrelationInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		id := ""
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if vals := md.Get(CorrelationMetadataKey); len(vals) > 0 {
				id = vals[0]
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

func newID() string {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.NewString()
	}
	return id.String()
}
