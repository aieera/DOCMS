package regionenforcer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func newTestEnforcer() *Enforcer {
	return New(Config{
		StorageEndpointByRegion: map[string]string{
			"us-east-1":  "dms-us-east-1-hot",
			"eu-west-1":  "dms-eu-west-1-hot",
			"me-south-1": "dms-me-south-1-hot",
		},
		SearchEndpointByRegion: map[string]string{
			"us-east-1": "documents-us-east-1-",
			"eu-west-1": "documents-eu-west-1-",
		},
		CacheKeyspaceByRegion: map[string]string{
			"eu-west-1": "r:eu-west-1:",
		},
	})
}

func TestEnforcer_ValidateMatchingEndpoint_OK(t *testing.T) {
	e := newTestEnforcer()
	require.NoError(t, e.Validate("eu-west-1", LayerStorage, "dms-eu-west-1-hot"))
}

func TestEnforcer_ValidateCrossBoundaryEndpoint_RejectsAsCrossBoundary(t *testing.T) {
	e := newTestEnforcer()
	err := e.Validate("eu-west-1", LayerStorage, "dms-us-east-1-hot")
	var rv *ErrRegionViolation
	require.True(t, errors.As(err, &rv))
	require.Equal(t, "cross_boundary", rv.Reason)
	require.Equal(t, "us-east-1", rv.TargetRegion)
}

func TestEnforcer_ValidateUnconfiguredLayer_LayerMismatch(t *testing.T) {
	e := newTestEnforcer()
	err := e.Validate("eu-west-1", LayerBackup, "anything")
	var rv *ErrRegionViolation
	require.True(t, errors.As(err, &rv))
	require.Equal(t, "layer_mismatch", rv.Reason)
}

func TestEnforcer_ValidateUnknownEndpoint_UnknownRegion(t *testing.T) {
	e := newTestEnforcer()
	err := e.Validate("eu-west-1", LayerStorage, "dms-nowhere-hot")
	var rv *ErrRegionViolation
	require.True(t, errors.As(err, &rv))
	require.Equal(t, "unknown_region", rv.Reason)
}

func TestEnforcer_EndpointFor(t *testing.T) {
	e := newTestEnforcer()
	require.Equal(t, "dms-eu-west-1-hot", e.EndpointFor("eu-west-1", LayerStorage))
	require.Equal(t, "", e.EndpointFor("ap-northeast-1", LayerStorage))
}

// --- Middleware ------------------------------------------------------------

func TestMiddleware_ForceRegionMismatch_Returns451WithBody(t *testing.T) {
	e := newTestEnforcer()
	mw := RegionEnforcerHTTP(MiddlewareOptions{
		Enforcer: e,
		Layer:    LayerStorage,
		Lookup: func(_ context.Context, _, _ string) (string, error) {
			return "eu-west-1", nil
		},
		PathParam: func(r *http.Request) string { return r.URL.Query().Get("doc") },
	})

	srv := httptest.NewServer(mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	defer srv.Close()

	req, _ := http.NewRequest("POST", srv.URL+"/?doc=abc", strings.NewReader("{}"))
	req.Header.Set("X-Force-Region", "us-east-1")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusUnavailableForLegalReasons, resp.StatusCode)
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, "REGION_VIOLATION", body["error_code"])
	d := body["details"].(map[string]any)
	require.Equal(t, "eu-west-1", d["document_region"])
	require.Equal(t, "us-east-1", d["target_region"])
	require.Equal(t, "cross_boundary", d["reason"])
}

func TestMiddleware_MatchingForceRegion_PassesThrough(t *testing.T) {
	e := newTestEnforcer()
	mw := RegionEnforcerHTTP(MiddlewareOptions{
		Enforcer:  e,
		Layer:     LayerStorage,
		Lookup:    func(_ context.Context, _, _ string) (string, error) { return "eu-west-1", nil },
		PathParam: func(r *http.Request) string { return r.URL.Query().Get("doc") },
	})

	called := false
	srv := httptest.NewServer(mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})))
	defer srv.Close()

	req, _ := http.NewRequest("POST", srv.URL+"/?doc=abc", strings.NewReader("{}"))
	req.Header.Set("X-Force-Region", "eu-west-1")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.True(t, called)
}

func TestMiddleware_NoDocID_PassesThrough(t *testing.T) {
	mw := RegionEnforcerHTTP(MiddlewareOptions{
		Enforcer:  newTestEnforcer(),
		Layer:     LayerStorage,
		Lookup:    func(_ context.Context, _, _ string) (string, error) { return "eu-west-1", nil },
		PathParam: func(_ *http.Request) string { return "" },
	})
	srv := httptest.NewServer(mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}
