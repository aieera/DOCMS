// Package webhook implements the webhook delivery worker with HMAC signing,
// retry with exponential backoff, and dead-lettering.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/services/connector/internal/model"
	"github.com/aieera/sedoc/services/connector/internal/repository"
)

// isInternalIP reports whether ip is in a range a webhook must never reach:
// loopback, RFC1918 private, link-local (unicast+multicast), unspecified
// (0.0.0.0 / ::), any multicast, and CGNAT 100.64.0.0/10 (RFC 6598, which
// net.IP.IsPrivate does NOT cover).
func isInternalIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil && ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
		return true
	}
	return false
}

// newSSRFSafeClient builds an http.Client whose transport rejects any
// connection to an internal IP AT DIAL TIME — the actual address dialed after
// DNS resolution. Because every hop (redirects AND fresh DNS resolutions) goes
// through this Control hook, it closes the TOCTOU gap between the create-time
// ValidateURL guard and delivery: DNS rebinding and redirect-to-internal are
// both caught here, not just at registration. allowPrivate (on-prem) disables
// the guard for trusted internal receivers.
func newSSRFSafeClient(timeout time.Duration, allowPrivate bool) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	if !allowPrivate {
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil || isInternalIP(ip) {
				return fmt.Errorf("blocked connection to internal address %q (SSRF guard)", address)
			}
			return nil
		}
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{DialContext: dialer.DialContext},
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("stopped after %d redirects", len(via))
			}
			return nil // each hop still dials through the guarded transport
		},
	}
}

// DeliveryWorker polls for pending deliveries and sends them.
type DeliveryWorker struct {
	repo  *repository.Repository
	log   zerolog.Logger
	hc    *http.Client
	stop  chan struct{}
	nudge chan struct{}
}

// NewDeliveryWorker creates a worker. allowPrivate must match the value passed
// to ValidateURL at registration so the delivery-time SSRF guard is consistent
// with the create-time guard.
func NewDeliveryWorker(repo *repository.Repository, log zerolog.Logger, allowPrivate bool) *DeliveryWorker {
	return &DeliveryWorker{
		repo: repo, log: log,
		stop:  make(chan struct{}),
		nudge: make(chan struct{}, 1),
		hc:    newSSRFSafeClient(10*time.Second, allowPrivate),
	}
}

// Start runs the delivery loop. Call in a goroutine.
func (w *DeliveryWorker) Start(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stop:
			return
		case <-ticker.C:
			w.processBatch(ctx)
		case <-w.nudge:
			// Wake-up triggered by an external caller (test-send,
			// redeliver) that just inserted a pending row and wants
			// it on the wire within sub-second. The poll interval
			// still guards crash-recovery; this is just for latency.
			w.processBatch(ctx)
		}
	}
}

// Kick signals the worker to drain pending deliveries on the next
// goroutine scheduling tick. Non-blocking — if a nudge is already
// queued (channel buffered at 1) the call is a no-op, because one
// pending wake-up will pick up everything that's pending.
func (w *DeliveryWorker) Kick() {
	select {
	case w.nudge <- struct{}{}:
	default:
	}
}

// Stop signals the worker to exit.
func (w *DeliveryWorker) Stop() {
	select {
	case <-w.stop:
	default:
		close(w.stop)
	}
}

func (w *DeliveryWorker) processBatch(ctx context.Context) {
	deliveries, err := w.repo.ListPendingDeliveries(ctx, 50)
	if err != nil {
		w.log.Error().Err(err).Msg("list pending deliveries")
		return
	}
	for _, d := range deliveries {
		w.deliver(ctx, d)
	}
}

func (w *DeliveryWorker) deliver(ctx context.Context, d *model.WebhookDelivery) {
	sub, err := w.repo.GetWebhook(ctx, d.TenantID, d.SubscriptionID)
	if err != nil || sub == nil || !sub.Active {
		d.DeadLettered = true
		_ = w.repo.UpdateDelivery(ctx, d)
		return
	}

	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	signature := signPayload(sub.Secret, timestamp, d.Payload)

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, sub.URL, bytes.NewReader(d.Payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-DMS-Signature", signature)
	req.Header.Set("X-DMS-Timestamp", timestamp)
	req.Header.Set("X-DMS-Event", d.EventType)
	req.Header.Set("User-Agent", "SeDoc-Webhook/1.0")

	resp, err := w.hc.Do(req)
	d.Attempts++
	now := time.Now().UTC()

	if err != nil {
		d.ResponseBody = err.Error()
		d.StatusCode = 0
	} else {
		d.StatusCode = resp.StatusCode
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		resp.Body.Close()
		d.ResponseBody = string(body)
	}

	if d.StatusCode >= 200 && d.StatusCode < 300 {
		d.DeliveredAt = &now
		w.log.Info().Str("id", d.ID).Int("status", d.StatusCode).Msg("webhook delivered")
	} else if d.Attempts >= len(model.RetryBackoff) {
		d.DeadLettered = true
		w.log.Warn().Str("id", d.ID).Int("attempts", d.Attempts).Msg("webhook dead-lettered")
	} else {
		nextRetry := now.Add(model.RetryBackoff[d.Attempts-1])
		d.NextRetryAt = &nextRetry
		w.log.Info().Str("id", d.ID).Int("attempt", d.Attempts).Time("retry_at", nextRetry).Msg("webhook retry scheduled")
	}
	_ = w.repo.UpdateDelivery(ctx, d)
}

// ValidateURL checks that a webhook URL is external (no internal IPs) and
// reachable. Returns an error if validation fails.
//
// allowPrivate relaxes the SSRF guard for self-hosted / on-prem deployments
// (cfg.WebhookAllowPrivateTargets): when true the URL may use plain http and
// may resolve to a private/loopback/link-local address — appropriate when the
// receiver lives on a trusted internal network (e.g. an internal ERP on a
// LAN). The DNS-resolvability and HEAD reachability checks always run, and the
// HMAC signature still authenticates every delivery regardless of this flag.
func ValidateURL(rawURL string, allowPrivate bool) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "https" && !(allowPrivate && u.Scheme == "http") {
		return fmt.Errorf("url must use https")
	}
	host := u.Hostname()
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("dns lookup failed: %w", err)
	}
	if !allowPrivate {
		for _, ip := range ips {
			if isInternalIP(ip) {
				return fmt.Errorf("url resolves to internal IP %s", ip)
			}
		}
	}
	// HEAD check through the SSRF-safe client so the reachability probe can't
	// itself be steered to an internal host via a redirect or a rebinding DNS
	// answer (the create-time LookupIP above is advisory; the dial-time guard
	// is authoritative).
	client := newSSRFSafeClient(5*time.Second, allowPrivate)
	resp, err := client.Head(rawURL)
	if err != nil {
		return fmt.Errorf("url not reachable: %w", err)
	}
	resp.Body.Close()
	return nil
}

func signPayload(secret, timestamp string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// silence unused
var _ = strings.TrimSpace
