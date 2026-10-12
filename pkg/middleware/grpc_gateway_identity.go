package middleware

import (
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/aieera/sedoc/pkg/auth"
)

// grpcMetadataPrefix is the request-header prefix grpc-gateway's default
// incoming matcher turns into gRPC metadata (prefix stripped, verbatim).
const grpcMetadataPrefix = "Grpc-Metadata-"

// StripGRPCMetadata removes every client-supplied Grpc-Metadata-* header.
// grpc-gateway forwards those verbatim as gRPC metadata, which the gRPC
// interceptors (TenantInterceptor, UserIdentityInterceptor,
// CorrelationInterceptor) trust — so a client could otherwise set
// x-tenant-id / x-user-role / x-client-ip directly. Use it on any route
// that hands a request to grpc-gateway without GatewayIdentityMetadata.
func StripGRPCMetadata(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stripGRPCMetadata(r.Header)
		next.ServeHTTP(w, r)
	})
}

func stripGRPCMetadata(h http.Header) {
	for k := range h {
		if strings.HasPrefix(http.CanonicalHeaderKey(k), grpcMetadataPrefix) {
			delete(h, k)
		}
	}
}

// GatewayIdentityMetadata is the document service's grpc-gateway identity
// injector (C-6, DEC-3). grpc-gateway dials the service's own gRPC port,
// so Go ctx values do not survive the hop; identity travels as
// Grpc-Metadata-* request headers that the gateway turns into the
// x-tenant-id / x-user-id / x-user-role / x-user-name metadata the
// interceptors read.
//
// Identity comes from the authenticated context ONLY (session, API key,
// internal caller — resolved by the middleware in front, with TenantHTTP
// having fixed the tenant). Inbound X-Tenant-ID / X-User-ID / X-User-Role
// headers are never copied, and every client-supplied Grpc-Metadata-*
// header is dropped first. Previously the raw headers were copied AHEAD of
// the session, so any caller could act as any user/role in any tenant on
// every gateway route.
//
// Client IP / user agent / correlation id are forwarded from ctx as well
// (StampGatewayClientMeta).
func GatewayIdentityMetadata() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			stripGRPCMetadata(r.Header)
			ctx := r.Context()
			user, userErr := auth.User(ctx)
			tid, terr := auth.GetTenantID(ctx)
			if (terr != nil || tid == uuid.Nil) && userErr == nil {
				tid = user.TenantID
			}
			if tid != uuid.Nil {
				r.Header.Set(grpcMetadataPrefix+TenantMetadataKey, tid.String())
			}
			if userErr == nil {
				if user.ID != uuid.Nil {
					r.Header.Set(grpcMetadataPrefix+UserMetadataKey, user.ID.String())
				}
				if user.Role != "" {
					r.Header.Set(grpcMetadataPrefix+UserRoleMetadataKey, user.Role)
				}
				if user.Email != "" {
					r.Header.Set(grpcMetadataPrefix+UserNameMetadataKey, user.Email)
				}
			}
			StampGatewayClientMeta(r)
			next.ServeHTTP(w, r)
		})
	}
}
