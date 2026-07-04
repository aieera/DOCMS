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
	"time"
)

// Forwarder delivers a normalised event to a sink. One instance is shared
// across the consumer; the HTTP client carries the per-request timeout.
type Forwarder struct {
	hc     *http.Client
	dialer func(ctx context.Context, network, addr string) (net.Conn, error)
}

func NewForwarder(hc *http.Client) *Forwarder {
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	d := &net.Dialer{Timeout: 5 * time.Second}
	return &Forwarder{hc: hc, dialer: d.DialContext}
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
