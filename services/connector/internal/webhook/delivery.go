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
	"time"

	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/services/connector/internal/model"
	"github.com/vaultdms/vaultdms/services/connector/internal/repository"
)

// DeliveryWorker polls for pending deliveries and sends them.
type DeliveryWorker struct {
	repo  *repository.Repository
	log   zerolog.Logger
	hc    *http.Client
	stop  chan struct{}
	nudge chan struct{}
}

// NewDeliveryWorker creates a worker.
func NewDeliveryWorker(repo *repository.Repository, log zerolog.Logger) *DeliveryWorker {
	return &DeliveryWorker{
		repo: repo, log: log,
		stop:  make(chan struct{}),
		nudge: make(chan struct{}, 1),
		hc:    &http.Client{Timeout: 10 * time.Second},
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
	req.Header.Set("User-Agent", "VaultDMS-Webhook/1.0")

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
func ValidateURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("url must use https")
	}
	host := u.Hostname()
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("dns lookup failed: %w", err)
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
			return fmt.Errorf("url resolves to internal IP %s", ip)
		}
	}
	// HEAD check to verify reachability.
	client := &http.Client{Timeout: 5 * time.Second}
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
