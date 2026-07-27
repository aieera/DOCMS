package siem

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"
)

// Forwarder delivers a normalised event to a sink. One instance is shared
// across the consumer; the HTTP client carries the per-request timeout.
type Forwarder struct {
	hc     *http.Client
	dialer func(ctx context.Context, network, addr string) (net.Conn, error)
}

// alwaysBlockedIP is never a legitimate SIEM target, even when private targets
// are permitted: link-local (169.254.0.0/16 — the cloud metadata endpoint),
// the unspecified address, and multicast.
func alwaysBlockedIP(ip net.IP) bool {
	return ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

// isInternalIP additionally covers loopback, RFC1918 private, and CGNAT
// (100.64.0.0/10) — blocked unless the deployment opts into private targets
// (on-prem SIEM on a trusted LAN).
func isInternalIP(ip net.IP) bool {
	if alwaysBlockedIP(ip) || ip.IsLoopback() || ip.IsPrivate() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil && ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
		return true
	}
	return false
}

// guardedDialer rejects connections to blocked IPs AT DIAL TIME — the actual
// address after DNS resolution — so redirects and DNS rebinding can't escape
// the check. allowPrivate relaxes the RFC1918/loopback/CGNAT block for on-prem
// internal SIEMs, but the metadata/link-local ranges stay blocked regardless.
func guardedDialer(allowPrivate bool) *net.Dialer {
	d := &net.Dialer{Timeout: 5 * time.Second}
	d.Control = func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return fmt.Errorf("cannot parse dial address %q", address)
		}
		if alwaysBlockedIP(ip) || (!allowPrivate && isInternalIP(ip)) {
			return fmt.Errorf("blocked connection to internal address %q (SIEM SSRF guard)", address)
		}
		return nil
	}
	return d
}

// NewForwarder builds a forwarder whose HTTP client AND syslog dialer both
// enforce the SSRF guard. Previously the HTTP client used the default transport
// (no dialer, no IP check, no redirect limit) and the syslog dialer was a plain
// net.Dialer, so a tenant-configured sink endpoint could reach cloud metadata /
// internal hosts. allowPrivate permits internal LAN targets (on-prem SIEM).
func NewForwarder(hc *http.Client, allowPrivate bool) *Forwarder {
	timeout := 10 * time.Second
	if hc != nil && hc.Timeout > 0 {
		timeout = hc.Timeout
	}
	d := guardedDialer(allowPrivate)
	client := &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{DialContext: d.DialContext},
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("stopped after %d redirects", len(via))
			}
			return nil // each hop still dials through the guarded transport
		},
	}
	return &Forwarder{hc: client, dialer: d.DialContext}
}

// Forward routes to the sink-type-specific delivery. A non-nil error means the
// caller should count a failure and (via the consumer) retry/DLQ.
func (f *Forwarder) Forward(ctx context.Context, sink Sink, ev NormalizedEvent) error {
	switch sink.Type {
	case SinkSplunk:
		return f.splunkHEC(ctx, sink, ev)
	case SinkSentinel:
		return f.sentinelHEC(ctx, sink, ev)
	case SinkSyslog:
		return f.syslog(ctx, sink, ev)
	default:
		return fmt.Errorf("unknown sink type %q", sink.Type)
	}
}

// splunkHEC posts to Splunk's HTTP Event Collector:
//
//	POST <endpoint>/services/collector/event
//	Authorization: Splunk <token>
//	{"event": <normalized>, "sourcetype": "sedoc:audit", "time": <epoch>}
func (f *Forwarder) splunkHEC(ctx context.Context, sink Sink, ev NormalizedEvent) error {
	url := strings.TrimRight(sink.Endpoint, "/")
	if !strings.Contains(url, "/services/collector") {
		url += "/services/collector/event"
	}
	body, _ := json.Marshal(map[string]any{
		"event":      ev,
		"sourcetype": "sedoc:audit",
		"source":     "sedoc",
		"time":       epoch(ev.Timestamp),
	})
	return f.doPost(ctx, url, body, "Splunk "+sink.Token)
}

// sentinelHEC posts to a Microsoft Sentinel ingestion endpoint (Logs Ingestion
// API / DCE) with a bearer token. (The legacy Data Collector API HMAC path is
// an alternative; bearer keeps the sink config to endpoint+token.)
func (f *Forwarder) sentinelHEC(ctx context.Context, sink Sink, ev NormalizedEvent) error {
	body, _ := json.Marshal([]NormalizedEvent{ev})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sink.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+sink.Token)
	req.Header.Set("Log-Type", "SedocAudit")
	return f.send(req)
}

func (f *Forwarder) doPost(ctx context.Context, url string, body []byte, auth string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	return f.send(req)
}

func (f *Forwarder) send(req *http.Request) error {
	resp, err := f.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("sink returned %s: %s", resp.Status, strings.TrimSpace(buf.String()))
	}
	return nil
}

// syslog sends one RFC 5424 message. Endpoint is "host:port" (TCP by default)
// or "udp://host:port". Facility 13 (log audit), severity 6 (info).
func (f *Forwarder) syslog(ctx context.Context, sink Sink, ev NormalizedEvent) error {
	network, addr := "tcp", sink.Endpoint
	if strings.HasPrefix(addr, "udp://") {
		network, addr = "udp", strings.TrimPrefix(addr, "udp://")
	} else if strings.HasPrefix(addr, "tcp://") {
		addr = strings.TrimPrefix(addr, "tcp://")
	}
	conn, err := f.dialer(ctx, network, addr)
	if err != nil {
		return fmt.Errorf("syslog dial: %w", err)
	}
	defer conn.Close()
	msg, _ := json.Marshal(ev)
	// <PRI>VERSION TIMESTAMP HOSTNAME APP-NAME PROCID MSGID SD MSG
	const pri = 13*8 + 6 // facility 13, severity 6
	line := fmt.Sprintf("<%d>1 %s sedoc audit - %s - %s\n",
		pri, ev.Timestamp, ev.Subject, msg)
	if network == "tcp" {
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	}
	_, err = conn.Write([]byte(line))
	return err
}

// epoch parses an RFC3339 timestamp to Unix seconds (Splunk's `time` field).
// Falls back to 0 (Splunk then stamps receive time).
func epoch(ts string) int64 {
	if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
		return t.Unix()
	}
	return 0
}
