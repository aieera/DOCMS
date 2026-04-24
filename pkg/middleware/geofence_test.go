package middleware

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/vaultdms/vaultdms/pkg/trustedproxy"
)

func testReq(remote string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remote
	return r
}

func TestGeofence_Allow(t *testing.T) {
	tid := uuid.New()
	called := false
	h := Geofence(GeofenceConfig{
		Decide: func(context.Context, uuid.UUID, *uuid.UUID, *uuid.UUID, GeofenceAction, net.IP) (GeofenceDecision, error) {
			return GeofenceDecision{Allow: true, Reason: "no_policy"}, nil
		},
		ResolveTenant: func(*http.Request) (uuid.UUID, bool) { return tid, true },
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, testReq("203.0.113.5:1234"))
	require.True(t, called)
	require.Equal(t, http.StatusOK, rr.Code)
}

func TestGeofence_Deny451(t *testing.T) {
	tid := uuid.New()
	h := Geofence(GeofenceConfig{
		Decide: func(context.Context, uuid.UUID, *uuid.UUID, *uuid.UUID, GeofenceAction, net.IP) (GeofenceDecision, error) {
			return GeofenceDecision{Allow: false, Reason: "country_deny"}, nil
		},
		ResolveTenant: func(*http.Request) (uuid.UUID, bool) { return tid, true },
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Fatal("must not reach") }))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, testReq("1.2.3.4:1"))
	require.Equal(t, http.StatusUnavailableForLegalReasons, rr.Code)
	require.Equal(t, "country_deny", rr.Header().Get("X-Geofence-Reason"))
}

func TestGeofence_StepUp428(t *testing.T) {
	tid := uuid.New()
	h := Geofence(GeofenceConfig{
		Decide: func(context.Context, uuid.UUID, *uuid.UUID, *uuid.UUID, GeofenceAction, net.IP) (GeofenceDecision, error) {
			return GeofenceDecision{Allow: true, RequireStepUp: true, Reason: "step_up_required"}, nil
		},
		ResolveTenant: func(*http.Request) (uuid.UUID, bool) { return tid, true },
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Fatal("must not reach") }))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, testReq("1.2.3.4:1"))
	require.Equal(t, http.StatusPreconditionRequired, rr.Code)
	require.Equal(t, "Step-Up", rr.Header().Get("WWW-Authenticate"))
}

func TestGeofence_FailClosed(t *testing.T) {
	tid := uuid.New()
	h := Geofence(GeofenceConfig{
		Decide: func(context.Context, uuid.UUID, *uuid.UUID, *uuid.UUID, GeofenceAction, net.IP) (GeofenceDecision, error) {
			return GeofenceDecision{}, http.ErrAbortHandler
		},
		ResolveTenant: func(*http.Request) (uuid.UUID, bool) { return tid, true },
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Fatal("must not reach on error") }))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, testReq("1.2.3.4:1"))
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestGeofence_NoTenantPassesThrough(t *testing.T) {
	h := Geofence(GeofenceConfig{
		Decide: func(context.Context, uuid.UUID, *uuid.UUID, *uuid.UUID, GeofenceAction, net.IP) (GeofenceDecision, error) {
			t.Fatal("decider should not be called when tenant is missing")
			return GeofenceDecision{}, nil
		},
		ResolveTenant: func(*http.Request) (uuid.UUID, bool) { return uuid.Nil, false },
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, testReq("1.2.3.4:1"))
	require.Equal(t, http.StatusOK, rr.Code)
}

func TestRealClientIP_XFF(t *testing.T) {
	// realClientIP now delegates to pkg/trustedproxy. The peer
	// (10.0.0.1) must be in the trusted-proxy CIDR list for XFF to
	// be believed; configure that explicitly here.
	cfg, err := trustedproxy.Parse("10.0.0.0/8")
	require.NoError(t, err)
	trustedproxy.SetDefault(cfg)
	t.Cleanup(func() { trustedproxy.SetDefault(trustedproxy.Config{}) })

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:9999"
	r.Header.Set("X-Forwarded-For", "8.8.8.8, 1.2.3.4")
	ip := trustedproxy.RealClientIPNetIP(r, trustedproxy.Default())
	require.Equal(t, "1.2.3.4", ip.String())
}
