// M-31 — GET /api/v1/admin/platform/db-info was mounted behind SessionAuth
// only (the handler comment claimed a role gate), so any tenant member —
// a viewer included — read the Postgres server version string and the
// platform capability matrix. Platform internals are for platform
// operators: the route now requires the tenant owner role AND an active
// platform_admins grant.
package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

type fakePlatformAdmins struct {
	admins map[uuid.UUID]bool
	err    error
}

func (f fakePlatformAdmins) IsPlatformAdmin(_ context.Context, userID uuid.UUID) (bool, error) {
	return f.admins[userID], f.err
}

func TestDBInfo_MemberForbidden(t *testing.T) {
	tenant := uuid.New()
	platformOwner, plainOwner, admin, member := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	checker := fakePlatformAdmins{admins: map[uuid.UUID]bool{platformOwner: true, admin: true, member: true}}

	h := NewDBInfoHandler(nil)
	h.admins = checker
	mux := http.NewServeMux()
	h.Register(mux)

	get := func(user uuid.UUID, role string) int {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, ztAsRole(httptest.NewRequest(http.MethodGet, "/api/v1/admin/platform/db-info", nil), tenant, user, role))
		return rec.Code
	}

	if c := get(member, "member"); c != http.StatusForbidden {
		t.Fatalf("member: want 403, got %d", c)
	}
	if c := get(uuid.New(), "viewer"); c != http.StatusForbidden {
		t.Fatalf("viewer: want 403, got %d", c)
	}
	if c := get(admin, "admin"); c != http.StatusForbidden {
		t.Fatalf("tenant admin (even a platform admin): want 403, got %d", c)
	}
	if c := get(plainOwner, "owner"); c != http.StatusForbidden {
		t.Fatalf("tenant owner without a platform grant: want 403, got %d", c)
	}
	if c := get(platformOwner, "owner"); c != http.StatusOK {
		t.Fatalf("owner with a platform grant: want 200, got %d", c)
	}

	// A failed grant lookup fails closed.
	h.admins = fakePlatformAdmins{err: errors.New("relation platform_admins does not exist")}
	if c := get(platformOwner, "owner"); c != http.StatusForbidden {
		t.Fatalf("grant lookup error: want 403, got %d", c)
	}
}
