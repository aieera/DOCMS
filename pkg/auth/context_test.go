package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// The context helpers are load-bearing for every RLS-gated query. If a
// getter returns zero when the setter was called correctly, RLS won't be
// set and the query either blows up or (worse) silently returns 0 rows.

func TestWithUser_RoundtripsFullUserInfo(t *testing.T) {
	tid := uuid.New()
	uid := uuid.New()
	gid := uuid.New()
	want := UserInfo{
		ID:       uid,
		TenantID: tid,
		Email:    "a@example.com",
		Role:     "owner",
		Groups:   []uuid.UUID{gid},
	}
	ctx := WithUser(context.Background(), want)

	got, err := User(ctx)
	if err != nil {
		t.Fatalf("User: %v", err)
	}
	if got.ID != want.ID {
		t.Errorf("ID: got %v, want %v", got.ID, want.ID)
	}
	if got.TenantID != want.TenantID {
		t.Errorf("TenantID: got %v, want %v", got.TenantID, want.TenantID)
	}
	if got.Email != want.Email {
		t.Errorf("Email: got %q, want %q", got.Email, want.Email)
	}
	if got.Role != want.Role {
		t.Errorf("Role: got %q, want %q", got.Role, want.Role)
	}
	if len(got.Groups) != 1 || got.Groups[0] != gid {
		t.Errorf("Groups: got %v", got.Groups)
	}
}

func TestUser_ReturnsErrMissingWhenAbsent(t *testing.T) {
	_, err := User(context.Background())
	if err == nil {
		t.Fatal("User on empty ctx should return an error")
	}
	if !errors.Is(err, ErrMissing) {
		t.Errorf("want ErrMissing, got %v", err)
	}
}

func TestGetTenantID_Symmetric(t *testing.T) {
	id := uuid.New()
	ctx := SetTenantID(context.Background(), id)
	got, err := GetTenantID(ctx)
	if err != nil {
		t.Fatalf("GetTenantID: %v", err)
	}
	if got != id {
		t.Errorf("got %v, want %v", got, id)
	}
}

func TestGetTenantID_PopulatedByWithUser(t *testing.T) {
	// WithUser stores the stringified tenantID under the same context key
	// as SetTenantID — so every RLS-setting middleware that reads the
	// tenant gets it regardless of which setter fired first.
	want := uuid.New()
	ctx := WithUser(context.Background(), UserInfo{TenantID: want, ID: uuid.New()})
	got, err := GetTenantID(ctx)
	if err != nil {
		t.Fatalf("GetTenantID: %v", err)
	}
	if got != want {
		t.Errorf("TenantID mismatch via WithUser: got %v, want %v", got, want)
	}
}

func TestGetTenantID_Missing(t *testing.T) {
	_, err := GetTenantID(context.Background())
	if err == nil {
		t.Fatal("should return error when tenant absent")
	}
}

// ---- correlation-id helpers ------------------------------------------------

func TestCorrelationID_Symmetric(t *testing.T) {
	ctx := SetCorrelationID(context.Background(), "corr-123")
	if got := GetCorrelationID(ctx); got != "corr-123" {
		t.Errorf("got %q, want corr-123", got)
	}
}

func TestCorrelationID_EmptyWhenAbsent(t *testing.T) {
	if got := GetCorrelationID(context.Background()); got != "" {
		t.Errorf("absent correlation id should be empty string, got %q", got)
	}
}

// ---- user helpers ----------------------------------------------------------

func TestGetUserID_FromWithUser(t *testing.T) {
	uid := uuid.New()
	ctx := WithUser(context.Background(), UserInfo{ID: uid, TenantID: uuid.New()})
	got, err := GetUserID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != uid {
		t.Errorf("got %v, want %v", got, uid)
	}
}

func TestGetUserID_MissingReturnsErr(t *testing.T) {
	_, err := GetUserID(context.Background())
	if err == nil {
		t.Fatal("should error when user absent")
	}
}

func TestGetUserRole_EmptyWhenAbsent(t *testing.T) {
	if got := GetUserRole(context.Background()); got != "" {
		t.Errorf("absent role should be empty, got %q", got)
	}
}

func TestGetUserRole_FromWithUser(t *testing.T) {
	ctx := WithUser(context.Background(), UserInfo{ID: uuid.New(), TenantID: uuid.New(), Role: "admin"})
	if got := GetUserRole(ctx); got != "admin" {
		t.Errorf("got %q, want admin", got)
	}
}

func TestGetUserGroups_NilWhenAbsent(t *testing.T) {
	if got := GetUserGroups(context.Background()); got != nil {
		t.Errorf("absent groups should be nil, got %v", got)
	}
}

func TestGetUserGroups_FromWithUser(t *testing.T) {
	g := []uuid.UUID{uuid.New(), uuid.New()}
	ctx := WithUser(context.Background(), UserInfo{ID: uuid.New(), TenantID: uuid.New(), Groups: g})
	got := GetUserGroups(ctx)
	if len(got) != 2 {
		t.Fatalf("want 2 groups, got %d", len(got))
	}
	if got[0] != g[0] || got[1] != g[1] {
		t.Errorf("groups mismatch: %v vs %v", got, g)
	}
}

// ErrMissing is exported and used across the codebase with errors.Is. Pin
// that contract so a refactor that wraps the sentinel keeps errors.Is
// working.
func TestErrMissing_IsExportedAndMatchable(t *testing.T) {
	if ErrMissing == nil {
		t.Fatal("ErrMissing must be non-nil")
	}
	_, err := User(context.Background())
	if !errors.Is(err, ErrMissing) {
		t.Errorf("User-on-empty error must satisfy errors.Is(err, ErrMissing)")
	}
}
