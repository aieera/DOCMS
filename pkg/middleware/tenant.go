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

// UserMetadataKey + UserRoleMetadataKey are the gRPC metadata keys
// the document/policy/storage services use for caller identity. The
// HTTP middleware chain on each service populates these via
// SessionAuth → outgoing metadata; UserIdentityInterceptor below
// reads them on the server side and stamps auth.UserInfo on ctx so
// every handler that calls auth.GetUserID / auth.GetUserRole gets
// real values. Without it, OPA Rule 5 / Rule 6 silently deny.
const (
	UserMetadataKey     = "x-user-id"
	UserRoleMetadataKey = "x-user-role"
	// UserNameMetadataKey carries the caller's human-readable
	// identifier (typically email). Optional — when present it lets
	// downstream services stamp audit_events.actor_name without
	// having to re-look-up the user from Postgres. Set by the auth
	// service when it issues outbound gRPC calls on behalf of an
	// authenticated session.
	UserNameMetadataKey = "x-user-name"
)

// UserIdentityInterceptor reads x-user-id + x-user-role from the
// inbound gRPC metadata and writes auth.UserInfo onto ctx. Pair with
// TenantInterceptor (which provides the TenantID this UserInfo is
// stamped against). Pulled forward from admin-security-posture
// (Wave 16 cross-service auth plumbing — see CLAUDE.md).
func UserIdentityInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return handler(ctx, req)
		}
		var (
			userID   uuid.UUID
			role     string
			userName string
		)
		if v := md.Get(UserMetadataKey); len(v) > 0 && v[0] != "" {
			if id, err := uuid.Parse(v[0]); err == nil {
				userID = id
			}
		}
		if v := md.Get(UserRoleMetadataKey); len(v) > 0 {
			role = v[0]
		}
		if v := md.Get(UserNameMetadataKey); len(v) > 0 {
			userName = v[0]
		}
		if userID == uuid.Nil && role == "" {
			return handler(ctx, req)
		}
		tenantID, _ := auth.GetTenantID(ctx)
		ctx = auth.WithUser(ctx, auth.UserInfo{
			ID:       userID,
			TenantID: tenantID,
			Email:    userName,
			Role:     role,
		})
		return handler(ctx, req)
	}
}

// TenantHTTP extracts the tenant from the request header (or, as a
// fallback, from an already-populated context — host-dev mode where
// SessionAuth ran first) and stores it on the context. Returns 401
// if neither source carries a usable tenant.
//
// FIX-9 (audit Section 14) — the previous implementation also tried
// to "validate" the tenant by acquiring a pool connection and
// running `SELECT set_config('app.current_tenant', $1, true)`, then
// releasing the connection BEFORE any real query ran. The GUC is
// transaction-local (third arg = true), so the moment the implicit
// transaction holding that connection ended, the GUC was gone. The
// next caller's repository acquired a fresh connection with no GUC
// set — every RLS-protected query relied entirely on
// database.WithTenantTx (which sets the GUC inside its own tx) for
// enforcement. The middleware block was dead code that implied a
// guarantee it didn't provide and invited future bugs.
//
// `pool` is kept in the signature for back-compat with the ~10
// callers in cmd/server/main.go; it's deliberately unused now.
// WithTenantTx is the only real enforcement boundary.
func TenantHTTP(_ *pgxpool.Pool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := r.Header.Get(TenantHeader)
			tid, err := uuid.Parse(raw)
			// Fallback: a preceding middleware (SessionAuth /
			// SessionAuthOptional) may have set the tenant on the
			// context from a session cookie. Browser-native loaders
			// (`<img src>`, `<video src>`, `<a download>`) can only
			// send cookies — they can't attach the X-Tenant-ID
			// header — so without this fallback every such request
			// 401's even when the user is logged in. Mirrors the
			// TenantInterceptor's gRPC-side fallback below.
			if err != nil || tid == uuid.Nil {
				if existing, e := auth.GetTenantID(r.Context()); e == nil && existing != uuid.Nil {
					tid = existing
					err = nil
				}
			}
			if err != nil || tid == uuid.Nil {
				writeUnauthorized(w, r, "missing or invalid tenant")
				return
			}
			ctx := auth.SetTenantID(r.Context(), tid)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// TenantInterceptor is the gRPC equivalent of TenantHTTP. See the
// FIX-9 comment on TenantHTTP for why `pool` is accepted but
// unused — WithTenantTx is the only real RLS enforcement boundary.
func TenantInterceptor(_ *pgxpool.Pool) grpc.UnaryServerInterceptor {
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
		return handler(ctx, req)
	}
}
