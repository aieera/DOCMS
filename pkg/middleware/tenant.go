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

// TenantHeaderAlt is the gateway-injected alias. Kong's request-transformer
// in prod writes both names; in host-dev the frontend mirrors that. Either
// is accepted so a single missing header doesn't 401 the whole request.
const TenantHeaderAlt = "X-Auth-Tenant-ID"

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
			if raw == "" {
				raw = r.Header.Get(TenantHeaderAlt)
			}
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

// UserMetadataKey + UserRoleMetadataKey are the gRPC metadata keys
// carrying user identity through the mesh. The HTTP gateway maps
// X-User-ID → x-user-id and X-User-Role → x-user-role via the
// `Grpc-Metadata-*` prefix shim in each service's main.go.
const (
	UserMetadataKey     = "x-user-id"
	UserRoleMetadataKey = "x-user-role"
)

// UserIdentityInterceptor reads x-user-id and x-user-role from the
// incoming gRPC metadata and populates a minimal auth.UserInfo on
// the context. Chain AFTER TenantInterceptor so TenantID is already
// set. Missing headers are NOT an error — some internal callers
// (outbox publishers, cron workers) have no user — we still set
// whatever we have.
//
// Without this interceptor, auth.GetUserID / GetUserRole return
// zero values inside gRPC handlers, which cascades into policy
// checks that never see a caller role and default-deny.
func UserIdentityInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return handler(ctx, req)
		}
		var (
			userID uuid.UUID
			role   string
		)
		if v := md.Get(UserMetadataKey); len(v) > 0 && v[0] != "" {
			if id, err := uuid.Parse(v[0]); err == nil {
				userID = id
			}
		}
		if v := md.Get(UserRoleMetadataKey); len(v) > 0 {
			role = v[0]
		}
		if userID == uuid.Nil && role == "" {
			return handler(ctx, req)
		}
		tenantID, _ := auth.GetTenantID(ctx)
		ctx = auth.WithUser(ctx, auth.UserInfo{
			ID:       userID,
			TenantID: tenantID,
			Role:     role,
		})
		return handler(ctx, req)
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
		// Fallback: TenantHTTP may have already set the tenant on
		// the context (host-dev mode where grpc-gateway's metadata
		// forwarding is lossy). Honour that instead of 401'ing.
		if err != nil || tid == uuid.Nil {
			if existing, e := auth.GetTenantID(ctx); e == nil && existing != uuid.Nil {
				tid = existing
				err = nil
			}
		}
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
