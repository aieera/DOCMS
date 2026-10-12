package middleware

import (
	"context"
	"net/http"

	"github.com/aieera/sedoc/pkg/auth"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// TenantHeader is the legacy HTTP header used for internal service-to-service
// tenant propagation. Public gateway traffic resolves the tenant from the
// session token instead.
const TenantHeader = "X-Tenant-ID"

// AuthTenantHeader is the canonical tenant header (audit M-7 / Track 3). The
// gateway + auth context populate it; readers prefer it and fall back to the
// legacy TenantHeader so the FE can drop the dual-write one release later.
const AuthTenantHeader = "X-Auth-Tenant-ID"

// TenantFromHeaders returns the request's tenant header value, preferring the
// canonical AuthTenantHeader and falling back to the legacy TenantHeader.
// Empty when neither is present.
func TenantFromHeaders(r *http.Request) string {
	if v := r.Header.Get(AuthTenantHeader); v != "" {
		return v
	}
	return r.Header.Get(TenantHeader)
}

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

// TenantHTTP stamps the request's tenant on the context and returns 401 if
// there is none (DEC-3, C-6).
//
// The authenticated context comes first: when a preceding middleware
// (SessionAuth*, APIKeyAuth, SessionOrAPIKey, IdentityHeadersHTTP) put a
// tenant on ctx, that is the tenant. A tenant header (X-Auth-Tenant-ID or
// the legacy X-Tenant-ID) is then only a consistency check — one naming a
// different tenant is refused with 403. It used to OVERRIDE the session,
// so a logged-in user could act in any tenant by sending a header.
//
// Without an authenticated identity a header tenant is honored only from
// an internal caller holding the service key (X-Internal-Service-Key);
// a cookieless request with a forged X-Tenant-ID gets 401.
//
// FIX-9 (audit Section 14): `pool` is kept in the signature for back-compat
// with the callers in cmd/server/main.go and is deliberately unused —
// database.WithTenantTx is the only real RLS enforcement boundary.
func TenantHTTP(_ *pgxpool.Pool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if tid, err := auth.GetTenantID(r.Context()); err == nil && tid != uuid.Nil {
				for _, h := range []string{AuthTenantHeader, TenantHeader} {
					raw := r.Header.Get(h)
					if raw == "" {
						continue
					}
					if hid, perr := uuid.Parse(raw); perr != nil || hid != tid {
						writeForbidden(w, r, "tenant header does not match the authenticated tenant")
						return
					}
				}
				next.ServeHTTP(w, r.WithContext(auth.SetTenantID(r.Context(), tid)))
				return
			}
			if validInternalServiceKey(r) {
				if tid, err := uuid.Parse(TenantFromHeaders(r)); err == nil && tid != uuid.Nil {
					next.ServeHTTP(w, r.WithContext(auth.SetTenantID(r.Context(), tid)))
					return
				}
			}
			writeUnauthorized(w, r, "missing or invalid tenant")
		})
	}
}

// TenantInterceptor is the gRPC equivalent of TenantHTTP: a tenant already
// on the context wins, and x-tenant-id metadata naming a different tenant
// is PermissionDenied; otherwise the metadata tenant (set by the calling
// service — gRPC ports are internal) is used. See TenantHTTP for why
// `pool` is accepted but unused.
func TenantInterceptor(_ *pgxpool.Pool) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		raw := ""
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if v := md.Get(TenantMetadataKey); len(v) > 0 {
				raw = v[0]
			}
		}
		if existing, e := auth.GetTenantID(ctx); e == nil && existing != uuid.Nil {
			if raw != "" {
				if mid, err := uuid.Parse(raw); err != nil || mid != existing {
					return nil, vdmserr.ToGRPCError(vdmserr.Forbidden("tenant metadata does not match the authenticated tenant"))
				}
			}
			return handler(auth.SetTenantID(ctx, existing), req)
		}
		tid, err := uuid.Parse(raw)
		if err != nil || tid == uuid.Nil {
			return nil, vdmserr.ToGRPCError(vdmserr.ErrUnauthorized)
		}
		ctx = auth.SetTenantID(ctx, tid)
		return handler(ctx, req)
	}
}
