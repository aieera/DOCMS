package handler

// Validation-path tests for the push-device API (ADR 0117). The happy
// paths need Postgres (repository-backed service) and live in the
// integration suite; these pin the auth gate and input validation,
// which reject before any service call.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/aieera/sedoc/pkg/auth"
)

func newDevicesMux(t *testing.T) *http.ServeMux {
	t.Helper()
	h := &Handler{}
	mux := http.NewServeMux()
	h.RegisterDevices(mux)
	return mux
}

func deviceReq(method, target, body string, authed bool) *http.Request {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, target, bytes.NewBufferString(body))
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	if authed {
		// Identity is context-only (auth.TenantIDString/UserIDString read
		// what SessionAuthOptional / IdentityHeadersHTTP stamped) —
		// inject it the way the middleware would.
		tid := uuid.MustParse("11111111-1111-1111-1111-111111111111")
		uid := uuid.MustParse("22222222-2222-2222-2222-222222222222")
		ctx := auth.SetTenantID(r.Context(), tid)
		ctx = auth.WithUser(ctx, auth.UserInfo{ID: uid, TenantID: tid})
		r = r.WithContext(ctx)
	}
	return r
}

func TestRegisterDevice_Unauthenticated401(t *testing.T) {
	mux := newDevicesMux(t)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, deviceReq(http.MethodPost, "/api/v1/notifications/devices", `{"token":"ExponentPushToken[x]"}`, false))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

func TestRegisterDevice_InvalidJSON400(t *testing.T) {
	mux := newDevicesMux(t)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, deviceReq(http.MethodPost, "/api/v1/notifications/devices", `{not json`, true))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestRegisterDevice_EmptyToken400(t *testing.T) {
	mux := newDevicesMux(t)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, deviceReq(http.MethodPost, "/api/v1/notifications/devices", `{"token":""}`, true))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestRegisterDevice_OverlongToken400(t *testing.T) {
	mux := newDevicesMux(t)
	body := `{"token":"` + strings.Repeat("x", 600) + `"}`
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, deviceReq(http.MethodPost, "/api/v1/notifications/devices", body, true))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestRegisterDevice_BadPlatform400(t *testing.T) {
	mux := newDevicesMux(t)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, deviceReq(http.MethodPost, "/api/v1/notifications/devices", `{"token":"t","platform":"pigeon"}`, true))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestListDevices_Unauthenticated401(t *testing.T) {
	mux := newDevicesMux(t)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, deviceReq(http.MethodGet, "/api/v1/notifications/devices", "", false))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

func TestDeleteDevice_Unauthenticated401(t *testing.T) {
	mux := newDevicesMux(t)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, deviceReq(http.MethodDelete, "/api/v1/notifications/devices/abc", "", false))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}
