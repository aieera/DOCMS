//go:build integration
// +build integration

// D-8 follow-up — a policy-service outage must not cost a sync client its
// documents. filterSyncDocuments failed closed by hiding the whole page,
// yet SyncDelta still returned (and persisted on the device) the advanced
// cursor, so the hidden documents were skipped forever. A policy error now
// fails the call (HTTP 503 + Retry-After) without moving the cursor.
package service_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/aieera/sedoc/pkg/auth"
	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
	"github.com/aieera/sedoc/services/document/internal/handler"
	"github.com/aieera/sedoc/services/document/internal/service"
)

// flakyPolicy allows everything, but its batch check fails while down.
type flakyPolicy struct{ down *atomic.Bool }

func (p flakyPolicy) CheckPermission(_ context.Context, _ *sedocv1.CheckPermissionRequest, _ ...grpc.CallOption) (*sedocv1.CheckPermissionResponse, error) {
	return &sedocv1.CheckPermissionResponse{Allowed: true}, nil
}
func (p flakyPolicy) BatchCheckPermission(_ context.Context, in *sedocv1.BatchCheckPermissionRequest, _ ...grpc.CallOption) (*sedocv1.BatchCheckPermissionResponse, error) {
	if p.down.Load() {
		return nil, errors.New("policy: connection refused")
	}
	out := &sedocv1.BatchCheckPermissionResponse{}
	for range in.GetChecks() {
		out.Results = append(out.Results, &sedocv1.CheckPermissionResponse{Allowed: true})
	}
	return out, nil
}

func TestSyncDelta_PolicyErrorDoesNotAdvanceCursor(t *testing.T) {
	h := newHarness(t, true)
	member := uuid.Must(uuid.NewV7())
	_, err := h.pool.Exec(h.ctx, `INSERT INTO users (tenant_id, id, email, display_name, role, status)
		VALUES ($1, $2, $3, 'U', 'member', 'active')`, h.tenant, member, member.String()+"@test.local")
	require.NoError(t, err)
	ws, root, doc := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	_, err = h.pool.Exec(h.ctx, `INSERT INTO workspaces (tenant_id, id, name, region_pin, created_by)
		VALUES ($1, $2, 'ws', 'us-east-1', $3)`, h.tenant, ws, h.user)
	require.NoError(t, err)
	_, err = h.pool.Exec(h.ctx, `INSERT INTO folders (tenant_id, id, workspace_id, name, path, depth)
		VALUES ($1, $2, $3, 'root', 'root', 0)`, h.tenant, root, ws)
	require.NoError(t, err)
	_, err = h.pool.Exec(h.ctx, `
		INSERT INTO documents (tenant_id, id, workspace_id, folder_id, title, description, region_pin, sha256_hash, mime_type)
		VALUES ($1, $2, $3, $4, 'handbook', '', 'us-east-1', '', 'application/pdf')`, h.tenant, doc, ws, root)
	require.NoError(t, err)

	down := &atomic.Bool{}
	svc := service.New(h.pool, h.repos, flakyPolicy{down: down}, zerolog.Nop())
	memberCtx := auth.WithUser(auth.SetTenantID(h.ctx, h.tenant),
		auth.UserInfo{ID: member, TenantID: h.tenant, Role: "member"})
	dev, err := svc.RegisterSyncDevice(memberCtx, "laptop", "linux", &ws)
	require.NoError(t, err)
	cursorOf := func() string {
		var c string
		require.NoError(t, h.pool.QueryRow(h.ctx, `SELECT COALESCE(cursor, '') FROM sync_devices WHERE tenant_id = $1 AND id = $2`,
			h.tenant, dev.ID).Scan(&c))
		return c
	}
	before := cursorOf()

	// Policy down: the call fails (service sentinel, HTTP 503 + Retry-After)
	// and the device cursor does not move.
	down.Store(true)
	_, err = svc.SyncDelta(memberCtx, &ws, &dev.ID, "", 100)
	require.ErrorIs(t, err, service.ErrPolicyUnavailable)
	require.Equal(t, before, cursorOf(), "a policy outage must not advance the device cursor")

	mux := http.NewServeMux()
	handler.NewSyncHandler(svc, zerolog.Nop()).Register(mux)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sync/delta?workspace_id="+ws.String()+"&device_id="+dev.ID.String(), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req.WithContext(memberCtx))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.NotEmpty(t, rec.Header().Get("Retry-After"))
	require.Equal(t, before, cursorOf())

	// Policy back: the same cursor yields the documents.
	down.Store(false)
	res, err := svc.SyncDelta(memberCtx, &ws, &dev.ID, before, 100)
	require.NoError(t, err)
	var ids []uuid.UUID
	for _, c := range res.Changes {
		ids = append(ids, c.ID)
	}
	require.Contains(t, ids, doc)
	require.NotEqual(t, before, cursorOf(), "a successful call advances the cursor")
}
