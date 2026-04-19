package middleware

import (
	"context"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vaultdms/vaultdms/pkg/auth"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// TenantHeader is the HTTP header used for internal service-to-service
// tenant propagation. Public gateway traffic resolves the tenant from the
// session token instead.
const TenantHeader = "X-Tenant-ID"

// TenantMetadataKey is the gRPC metadata equivalent.
const TenantMetadataKey = "x-tenant-id"

// TenantHTTP extracts the tenant from the request header, stores it on the
// context, and optionally validates it by running SET app.current_tenant on
// a pool connection. If the tenant is missing or malformed, the request is
// rejected with 401.
//
// pool is optional: pass nil to skip the RLS set_config (useful for services
// that don't own a Postgres connection, e.g. pure proxies). In that case the
// tenant is only attached to the context; services that subsequently use
// database.WithTenant will set the GUC themselves.
func TenantHTTP(pool *pgxpool.Pool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := r.Header.Get(TenantHeader)
			tid, err := uuid.Parse(raw)
			if err != nil || tid == uuid.Nil {
				writeUnauthorized(w, r, "missing or invalid tenant")
				return
			}
			ctx := auth.SetTenantID(r.Context(), tid)

			if pool != nil {
				conn, err := pool.Acquire(ctx)
				if err != nil {
					writeInternal(w, r, "acquire conn")
					return
				}
				if _, err := conn.Exec(ctx,
					"SELECT set_config('app.current_tenant', $1, true)",
					tid.String(),
				); err != nil {
					conn.Release()
					writeInternal(w, r, "set tenant")
					return
				}
				conn.Release()
			}

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// TenantInterceptor is the gRPC equivalent of TenantHTTP.
func TenantInterceptor(pool *pgxpool.Pool) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		raw := ""
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if v := md.Get(TenantMetadataKey); len(v) > 0 {
				raw = v[0]
			}
		}
		tid, err := uuid.Parse(raw)
		if err != nil || tid == uuid.Nil {
			return nil, vdmserr.ToGRPCError(vdmserr.ErrUnauthorized)
		}
		ctx = auth.SetTenantID(ctx, tid)

		if pool != nil {
			conn, err := pool.Acquire(ctx)
			if err != nil {
				return nil, vdmserr.ToGRPCError(vdmserr.ErrInternal)
			}
			if _, err := conn.Exec(ctx,
				"SELECT set_config('app.current_tenant', $1, true)",
				tid.String(),
			); err != nil {
				conn.Release()
				return nil, vdmserr.ToGRPCError(vdmserr.ErrInternal)
			}
			conn.Release()
		}

		return handler(ctx, req)
	}
}
