// Package middleware holds HTTP and gRPC middleware used by every VaultDMS
// service. Each middleware is provided in both transport flavors; they share
// the same context keys defined in pkg/logger.
package middleware

import (
	"context"
	"net/http"

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
func CorrelationHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(CorrelationHeader)
		if id == "" {
			id = newID()
		}
		w.Header().Set(CorrelationHeader, id)
		ctx := auth.SetCorrelationID(r.Context(), id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
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
