//go:build integration
// +build integration

// D-12 — the WOPI identity context.
//
// WOPI calls arrive from the editor with no session; the access token is
// the caller. The resolver rebuilt the caller as {user, tenant} only — no
// role, no groups — so every policy check it made (EnsureCanViewDocument on
// CheckFileInfo/GetFile, CreateVersion on PutFile) evaluated an anonymous-
// looking member: tenant admins (policy Rule 6 keys on the role) and users
// whose access comes through a group got 404 in the editor. The token now
// carries the role coauth/start saw on the session, and the resolver loads
// the user's groups inside the tenant transaction.
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/aieera/sedoc/pkg/auth"
	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
	"github.com/aieera/sedoc/services/document/internal/repository"
	"github.com/aieera/sedoc/services/document/internal/service"
)

// identityPolicy decides from the identity the document service hands it,
// like the real policy: org owner/admin (Rule 6) or membership of the
// granted group.
type identityPolicy struct{ grantedGroup uuid.UUID }

func (p identityPolicy) allow(ctx context.Context) bool {
	switch auth.GetUserRole(ctx) {
	case "owner", "admin":
		return true
	}
	for _, g := range auth.GetUserGroups(ctx) {
		if g == p.grantedGroup {
			return true
		}
	}
	return false
}

func (p identityPolicy) CheckPermission(ctx context.Context, _ *sedocv1.CheckPermissionRequest, _ ...grpc.CallOption) (*sedocv1.CheckPermissionResponse, error) {
	return &sedocv1.CheckPermissionResponse{Allowed: p.allow(ctx)}, nil
}
func (p identityPolicy) BatchCheckPermission(ctx context.Context, in *sedocv1.BatchCheckPermissionRequest, _ ...grpc.CallOption) (*sedocv1.BatchCheckPermissionResponse, error) {
	out := &sedocv1.BatchCheckPermissionResponse{}
	for range in.GetChecks() {
		out.Results = append(out.Results, &sedocv1.CheckPermissionResponse{Allowed: p.allow(ctx)})
	}
	return out, nil
}

func TestWOPIResolve_GroupGrantedEditor(t *testing.T) {
	const wopiSecret = "wopi-identity-secret"
	t.Setenv("SEDOC_WOPI_SECRET", wopiSecret)
	ctx, _, pool := newZTDB(t)
	tenant, groupEditor, doc, ver := ztSeedTenant(ctx, t, pool) // groupEditor: role member

	admin, outsider, group := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	for _, u := range []struct {
		id   uuid.UUID
		role string
	}{{admin, "admin"}, {outsider, "member"}} {
		_, err := pool.Exec(ctx, `INSERT INTO users (tenant_id, id, email, display_name, role, status)
			VALUES ($1, $2, $3, 'U', $4, 'active')`, tenant, u.id, u.id.String()+"@test.local", u.role)
		require.NoError(t, err)
	}
	_, err := pool.Exec(ctx, `INSERT INTO groups (tenant_id, id, name) VALUES ($1, $2, 'Editors')`, tenant, group)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO group_members (tenant_id, group_id, user_id) VALUES ($1, $2, $3)`,
		tenant, group, groupEditor)
	require.NoError(t, err)

	svc := service.New(pool, repository.New(pool), identityPolicy{grantedGroup: group}, zerolog.Nop())
	resolver := NewDBWOPIResolver(pool, nil, nil, nil, svc, zerolog.Nop())
	wopi := NewWOPIHandler(nil, zerolog.Nop())
	wopi.FileResolver = resolver
	mux := http.NewServeMux()
	wopi.Register(mux)

	// Tokens are minted by the real coauth/start path from a session.
	mint := func(user uuid.UUID, role string, groups ...uuid.UUID) string {
		h := NewCoauthStartHandler(svc, zerolog.Nop())
		t.Setenv("SEDOC_COAUTH_PROVIDER", "collabora")
		t.Setenv("SEDOC_COAUTH_URL", "http://collabora.test")
		cm := http.NewServeMux()
		h.Register(cm)
		req := httptest.NewRequest(http.MethodPost,
			"/api/v1/documents/"+doc.String()+"/versions/"+ver.String()+"/coauth/start", nil)
		req = req.WithContext(auth.WithUser(auth.SetTenantID(req.Context(), tenant),
			auth.UserInfo{ID: user, TenantID: tenant, Role: role, Groups: groups})) // as SessionAuth stamps it
		rec := httptest.NewRecorder()
		cm.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var out struct {
			AccessToken string `json:"access_token"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return out.AccessToken
	}
	checkFileInfo := func(token string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
			"/wopi/files/"+ver.String()+"?access_token="+url.QueryEscape(token), nil))
		return rec
	}

	// Group-granted editor: the editor opens the file and may write.
	rec := checkFileInfo(mint(groupEditor, "member", group))
	require.Equal(t, http.StatusOK, rec.Code, "group-granted editor must resolve: %s", rec.Body.String())
	var info WOPIFileInfo
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &info))
	require.True(t, info.UserCanWrite)

	// Tenant admin (Rule 6 on the role carried by the token).
	rec = checkFileInfo(mint(admin, "admin"))
	require.Equal(t, http.StatusOK, rec.Code, "tenant admin must resolve: %s", rec.Body.String())

	// No grant, no role: still 404 (and coauth/start would not have minted
	// one — mint a token directly to prove the resolver gate holds).
	tok := IssueWOPIToken(wopiSecret, WOPIClaims{
		TenantID: tenant, UserID: outsider, FileID: ver, DocumentID: doc,
		ExpiresAt: time.Now().Add(time.Hour), CanWrite: true, Role: "member",
	})
	require.Equal(t, http.StatusNotFound, checkFileInfo(tok).Code)

	// A token cannot smuggle a delimiter into the role.
	forged := IssueWOPIToken(wopiSecret, WOPIClaims{
		TenantID: tenant, UserID: outsider, FileID: ver, DocumentID: doc,
		ExpiresAt: time.Now().Add(time.Hour), CanWrite: true, Role: "member|admin",
	})
	require.Equal(t, http.StatusNotFound, checkFileInfo(forged).Code)
}

// D-12 follow-up — the resolver trusted the role minted into the token and
// never checked the user still exists or is active. A user suspended (or
// demoted, or deleted) after coauth/start kept full editor access for the
// token's lifetime. The resolver now reads the user in the tenant tx:
// missing/inactive → denied; the CURRENT role is used.
func TestWOPIResolve_SuspendedUserDenied(t *testing.T) {
	const wopiSecret = "wopi-identity-secret"
	t.Setenv("SEDOC_WOPI_SECRET", wopiSecret)
	ctx, _, pool := newZTDB(t)
	tenant, editor, doc, ver := ztSeedTenant(ctx, t, pool)
	group := uuid.Must(uuid.NewV7())
	admin, gone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	for _, u := range []uuid.UUID{admin, gone} {
		_, err := pool.Exec(ctx, `INSERT INTO users (tenant_id, id, email, display_name, role, status)
			VALUES ($1, $2, $3, 'U', 'admin', 'active')`, tenant, u, u.String()+"@test.local")
		require.NoError(t, err)
	}
	_, err := pool.Exec(ctx, `INSERT INTO groups (tenant_id, id, name) VALUES ($1, $2, 'Editors')`, tenant, group)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO group_members (tenant_id, group_id, user_id) VALUES ($1, $2, $3)`, tenant, group, editor)
	require.NoError(t, err)

	svc := service.New(pool, repository.New(pool), identityPolicy{grantedGroup: group}, zerolog.Nop())
	wopi := NewWOPIHandler(nil, zerolog.Nop())
	wopi.FileResolver = NewDBWOPIResolver(pool, nil, nil, nil, svc, zerolog.Nop())
	mux := http.NewServeMux()
	wopi.Register(mux)
	token := func(user uuid.UUID, role string) string {
		return IssueWOPIToken(wopiSecret, WOPIClaims{
			TenantID: tenant, UserID: user, FileID: ver, DocumentID: doc,
			ExpiresAt: time.Now().Add(time.Hour), CanWrite: true, Role: role,
		})
	}
	check := func(tok string) int {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
			"/wopi/files/"+ver.String()+"?access_token="+url.QueryEscape(tok), nil))
		return rec.Code
	}

	editorTok, adminTok, goneTok := token(editor, "member"), token(admin, "admin"), token(gone, "admin")
	require.Equal(t, http.StatusOK, check(editorTok))
	require.Equal(t, http.StatusOK, check(adminTok))
	require.Equal(t, http.StatusOK, check(goneTok))

	_, err = pool.Exec(ctx, `UPDATE users SET status = 'suspended' WHERE tenant_id = $1 AND id = $2`, tenant, editor)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, check(editorTok), "a suspended user's token must stop working")

	_, err = pool.Exec(ctx, `UPDATE users SET role = 'member' WHERE tenant_id = $1 AND id = $2`, tenant, admin)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, check(adminTok), "a demoted admin must lose the role-based grant")

	_, err = pool.Exec(ctx, `UPDATE users SET deleted_at = now() WHERE tenant_id = $1 AND id = $2`, tenant, gone)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, check(goneTok), "a deleted user's token must stop working")
}
