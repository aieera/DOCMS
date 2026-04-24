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
	"context"
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

	"go.temporal.io/sdk/client"

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
	mux.HandleFunc("GET /api/v1/platform/schedules", h.platformSchedules)
	mux.HandleFunc("GET /api/v1/platform/security/posture", h.platformSecurityPosture)
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

// ---- Schedules listing ---------------------------------------------------

// ScheduleView is the wire shape the admin UI consumes. One object per
// schedule in the Temporal namespace. Fields are nullable strings so
// the UI can render "—" for fresh schedules that have never fired.
type ScheduleView struct {
	ID           string  `json:"id"`
	Paused       bool    `json:"paused"`
	NextRun      *string `json:"next_run,omitempty"`   // RFC3339
	LastRun      *string `json:"last_run,omitempty"`   // RFC3339
	NumActions   int     `json:"num_actions"`          // total triggers so far
	NumMissed    int     `json:"num_missed,omitempty"` // missed catchup window
	RunningCount int     `json:"running_count"`        // currently-executing invocations
}

type schedulesResponse struct {
	Schedules []ScheduleView `json:"schedules"`
}

// platformSchedules enumerates every Temporal schedule in the
// workflow service's namespace and returns a compact view. Admin-only.
// Returns 503 when the Temporal client is unconfigured so the UI can
// render "Temporal not reachable" rather than an empty table.
func (h *Handler) platformSchedules(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if h.tc == nil {
		writeError(w, http.StatusServiceUnavailable, "temporal client not configured")
		return
	}

	cctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	sc := h.tc.ScheduleClient()
	iter, err := sc.List(cctx, client.ScheduleListOptions{PageSize: 100})
	if err != nil {
		h.log.Error().Err(err).Msg("platform schedules list")
		writeError(w, http.StatusBadGateway, "temporal list failed")
		return
	}

	out := make([]ScheduleView, 0, 16)
	for iter.HasNext() {
		entry, err := iter.Next()
		if err != nil {
			h.log.Error().Err(err).Msg("platform schedules iter")
			writeError(w, http.StatusBadGateway, "temporal iter failed")
			return
		}
		view, err := describeSchedule(cctx, sc, entry.ID)
		if err != nil {
			// Describe errors on one schedule must not abort the
			// listing — emit a row with the id and zero counters so
			// the UI still shows it.
			h.log.Warn().Err(err).Str("id", entry.ID).Msg("platform schedule describe")
			out = append(out, ScheduleView{ID: entry.ID})
			continue
		}
		out = append(out, view)
	}
	writeJSON(w, http.StatusOK, schedulesResponse{Schedules: out})
}

// describeSchedule pulls the per-schedule detail we surface to the UI.
// Kept separate so the listing loop stays compact.
func describeSchedule(ctx context.Context, sc client.ScheduleClient, id string) (ScheduleView, error) {
	desc, err := sc.GetHandle(ctx, id).Describe(ctx)
	if err != nil {
		return ScheduleView{}, err
	}
	v := ScheduleView{
		ID:           id,
		Paused:       desc.Schedule.State.Paused,
		NumActions:   desc.Info.NumActions,
		NumMissed:    desc.Info.NumActionsMissedCatchupWindow,
		RunningCount: len(desc.Info.RunningWorkflows),
	}
	if len(desc.Info.NextActionTimes) > 0 {
		s := desc.Info.NextActionTimes[0].UTC().Format(time.RFC3339)
		v.NextRun = &s
	}
	if len(desc.Info.RecentActions) > 0 {
		last := desc.Info.RecentActions[len(desc.Info.RecentActions)-1]
		s := last.ActualTime.UTC().Format(time.RFC3339)
		v.LastRun = &s
	}
	return v, nil
}

// ---- Security posture aggregator ----------------------------------------

// EnvAuditURL is the HTTP base for the audit service. Unset → the
// posture endpoint returns 503, matching the other "backend not
// configured" paths.
const EnvAuditURL = "VAULTDMS_AUDIT_URL"

// posturePlaceholderUnknown represents a gate that has no rows yet —
// CI has never run, or was never wired. Rendered as "unknown" by the
// UI with neutral tone, not red.
const posturePlaceholderUnknown = "unknown"

// SecurityPostureScan is the per-gate row the UI renders.
type SecurityPostureScan struct {
	ScanType      string     `json:"scan_type"`
	Status        string     `json:"status"` // pass | fail | unknown
	CriticalCount int        `json:"critical_count"`
	HighCount     int        `json:"high_count"`
	RunID         string     `json:"run_id,omitempty"`
	RunURL        string     `json:"run_url,omitempty"`
	RanAt         *time.Time `json:"ran_at,omitempty"`
}

// SecurityPosture is the endpoint's aggregate return shape. The UI
// banner lights up when `.AnyFailing` is true; the per-card view
// iterates `.Scans`.
type SecurityPosture struct {
	AnyFailing bool                  `json:"any_failing"`
	Scans      []SecurityPostureScan `json:"scans"`
}

// scanTypes is the canonical order the UI expects. Also used to
// backfill "unknown" rows for gates the audit service hasn't seen
// yet.
var scanTypes = []string{"sast", "dep_scan", "dast", "secret_scan"}

type auditLatestResponse struct {
	Scans []struct {
		ScanType      string    `json:"scan_type"`
		Status        string    `json:"status"`
		CriticalCount int       `json:"critical_count"`
		HighCount     int       `json:"high_count"`
		RunID         string    `json:"run_id,omitempty"`
		RunURL        string    `json:"run_url,omitempty"`
		RanAt         time.Time `json:"ran_at"`
	} `json:"scans"`
}

// platformSecurityPosture aggregates the audit service's latest-per-type
// rows into the shape the admin banner + /admin/platform/security page
// consume. Admin-only; 503 if the audit URL is unset.
func (h *Handler) platformSecurityPosture(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	auditURL := strings.TrimRight(os.Getenv(EnvAuditURL), "/")
	if auditURL == "" {
		writeError(w, http.StatusServiceUnavailable, "audit service URL not configured")
		return
	}

	cctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(cctx, http.MethodGet,
		auditURL+"/api/v1/audit/security-scans/latest", nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "build audit request failed")
		return
	}
	req.Header.Set("X-User-Role", r.Header.Get("X-User-Role"))

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		h.log.Error().Err(err).Str("audit_url", auditURL).Msg("posture fetch")
		writeError(w, http.StatusBadGateway, "audit fetch failed")
		return
	}
	defer resp.Body.Close()

	var latest auditLatestResponse
	if err := json.NewDecoder(resp.Body).Decode(&latest); err != nil {
		writeError(w, http.StatusBadGateway, "audit response decode failed")
		return
	}

	// Build a keyed map for O(1) lookup, then emit in the canonical
	// scan-type order with "unknown" placeholders for missing gates.
	by := make(map[string]SecurityPostureScan, len(latest.Scans))
	for _, s := range latest.Scans {
		ran := s.RanAt
		by[s.ScanType] = SecurityPostureScan{
			ScanType: s.ScanType, Status: s.Status,
			CriticalCount: s.CriticalCount, HighCount: s.HighCount,
			RunID: s.RunID, RunURL: s.RunURL,
			RanAt: &ran,
		}
	}

	out := SecurityPosture{Scans: make([]SecurityPostureScan, 0, len(scanTypes))}
	for _, st := range scanTypes {
		if v, ok := by[st]; ok {
			out.Scans = append(out.Scans, v)
			if v.Status == "fail" {
				out.AnyFailing = true
			}
			continue
		}
		out.Scans = append(out.Scans, SecurityPostureScan{
			ScanType: st,
			Status:   posturePlaceholderUnknown,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// Static check so a build fails if fmt becomes unused after refactor.
var _ = fmt.Sprint
