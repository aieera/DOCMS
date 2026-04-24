// Platform admin endpoints: cross-service observability for operators
// who need one surface to look at the /internal/* auth plane. Lives in
// the workflow service because (a) it already has an HTTP listener
// mounted at /api/v1/* through the standard auth chain, and (b) the
// alternative — a new platform service — is scope creep until there
// are more platform endpoints to share it.
//
// Routes:
//   POST /api/v1/platform/metrics/query
//     Admin-only PromQL passthrough to VAULTDMS_PROMETHEUS_URL. The
//     browser is not allowed to hit Prometheus directly because Prom
//     has no authentication of its own; this handler is the trust
//     boundary.
//
//   POST /api/v1/platform/trusted-proxy/test
//     Dry-run tester for pkg/trustedproxy — operators paste an XFF
//     chain and a RemoteAddr and get back the resolved client IP plus
//     a per-hop trusted/skipped breakdown. Read-only; editing CIDRs
//     is a ConfigMap change.

package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/vaultdms/vaultdms/pkg/trustedproxy"
)

// EnvPrometheusURL is the upstream Prometheus base (e.g.
// "http://prometheus:9090"). Unset → the endpoint responds 503 so the
// admin panel can render "metrics backend not configured" instead of
// a stale or empty page.
const EnvPrometheusURL = "VAULTDMS_PROMETHEUS_URL"

// RegisterPlatform mounts the platform admin routes on an already-
// constructed mux. Kept separate from Register so main.go can compose
// the two — platform routes can be disabled in deployments that don't
// need them.
func (h *Handler) RegisterPlatform(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/platform/metrics/query", h.platformMetricsQuery)
	mux.HandleFunc("POST /api/v1/platform/trusted-proxy/test", h.platformTrustedProxyTest)
}

// requireAdmin rejects any caller whose X-User-Role is not admin or
// owner. Mirrors the pattern in services/document's compliance handler
// (requireRole). Role is populated by pkg/middleware.UserIdentityInterceptor
// on gRPC and pkg/middleware.SessionAuth on HTTP.
func requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	role := r.Header.Get("X-User-Role")
	if role == "admin" || role == "owner" {
		return true
	}
	writeError(w, http.StatusForbidden, "admin role required")
	return false
}

type metricsQueryBody struct {
	// PromQL query. Required.
	Query string `json:"query"`
	// Optional: evaluation timestamp (RFC3339). Defaults to now.
	Time string `json:"time,omitempty"`
}

type metricsQueryResponse struct {
	// Raw Prometheus response forwarded as-is so the frontend can
	// consume the standard shape ({ status, data: { resultType, result } }).
	// Kept as json.RawMessage to avoid re-encoding and dropping fields.
	Upstream json.RawMessage `json:"upstream"`
}

func (h *Handler) platformMetricsQuery(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	promURL := strings.TrimRight(os.Getenv(EnvPrometheusURL), "/")
	if promURL == "" {
		writeError(w, http.StatusServiceUnavailable, "prometheus not configured")
		return
	}
	var body metricsQueryBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if strings.TrimSpace(body.Query) == "" {
		writeError(w, http.StatusBadRequest, "query required")
		return
	}

	q := url.Values{}
	q.Set("query", body.Query)
	if body.Time != "" {
		q.Set("time", body.Time)
	}
	upstream := promURL + "/api/v1/query?" + q.Encode()

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstream, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "upstream request build failed")
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		h.log.Error().Err(err).Str("upstream", promURL).Msg("prometheus query failed")
		writeError(w, http.StatusBadGateway, "prometheus upstream failed")
		return
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20)) // 4 MiB cap
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream read failed")
		return
	}
	// Forward the upstream status so Prometheus 4xx (bad query) is
	// visible to the operator.
	writeJSON(w, resp.StatusCode, metricsQueryResponse{Upstream: raw})
}

type trustedProxyTestBody struct {
	XForwardedFor string `json:"x_forwarded_for"`
	RemoteAddr    string `json:"remote_addr"`
}

type trustedProxyHopResult struct {
	Addr    string `json:"addr"`
	Trusted bool   `json:"trusted"`
	// Parseable flags a hop value we could not turn into an IP.
	// Useful for surfacing "you pasted a bad XFF" in the UI.
	Parseable bool `json:"parseable"`
}

type trustedProxyTestResponse struct {
	ResolvedIP string                  `json:"resolved_ip"`
	Peer       trustedProxyHopResult   `json:"peer"`
	Hops       []trustedProxyHopResult `json:"hops"`
	// TrustedCIDRs is the current config echo — the UI renders this
	// read-only so operators can sanity-check what the server sees.
	TrustedCIDRs []string `json:"trusted_cidrs"`
}

func (h *Handler) platformTrustedProxyTest(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var body trustedProxyTestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body.RemoteAddr == "" {
		writeError(w, http.StatusBadRequest, "remote_addr required")
		return
	}

	cfg := trustedproxy.Default()
	cidrs := make([]string, 0, len(cfg.TrustedCIDRs))
	for _, pfx := range cfg.TrustedCIDRs {
		cidrs = append(cidrs, pfx.String())
	}

	// Synthesise a request mirroring what the real middleware would
	// see and run it through the same resolver — so the UI answer is
	// definitionally the same as production's.
	synth := httptest.NewRequest(http.MethodGet, "/", nil)
	synth.RemoteAddr = body.RemoteAddr
	if body.XForwardedFor != "" {
		synth.Header.Set("X-Forwarded-For", body.XForwardedFor)
	}
	resolved := trustedproxy.RealClientIP(synth, cfg)

	peer := parseHop(body.RemoteAddr, true /* may include port */)
	if peer.Parseable {
		peer.Trusted = cfg.Trusts(mustAddr(peer.Addr))
	}

	var hops []trustedProxyHopResult
	if body.XForwardedFor != "" {
		for _, raw := range strings.Split(body.XForwardedFor, ",") {
			h := parseHop(strings.TrimSpace(raw), false)
			if h.Parseable {
				h.Trusted = cfg.Trusts(mustAddr(h.Addr))
			}
			hops = append(hops, h)
		}
	}

	resp := trustedProxyTestResponse{
		ResolvedIP:   "",
		Peer:         peer,
		Hops:         hops,
		TrustedCIDRs: cidrs,
	}
	if resolved.IsValid() {
		resp.ResolvedIP = resolved.String()
	}
	writeJSON(w, http.StatusOK, resp)
}

// parseHop returns a result with Addr set to the normalised IP
// string (host-only, no port) when parseable.
func parseHop(raw string, mayHavePort bool) trustedProxyHopResult {
	if raw == "" {
		return trustedProxyHopResult{Addr: "", Parseable: false}
	}
	host := raw
	if mayHavePort {
		// net/netip.ParseAddrPort handles "ip:port" and "[v6]:port".
		if ap, err := netip.ParseAddrPort(raw); err == nil {
			host = ap.Addr().String()
		}
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return trustedProxyHopResult{Addr: raw, Parseable: false}
	}
	if addr.Is4In6() {
		addr = addr.Unmap()
	}
	return trustedProxyHopResult{Addr: addr.String(), Parseable: true}
}

func mustAddr(s string) netip.Addr {
	a, _ := netip.ParseAddr(s)
	return a
}

// Static check so a build fails if fmt becomes unused after refactor.
var _ = fmt.Sprint
