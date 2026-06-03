package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// §10.8 / E8 — Slack channel adapter (incoming-webhook variant).
//
// Why incoming-webhook first:
//   - No OAuth round-trip, no per-tenant app install, no scope
//     negotiation. Tenant admin pastes a Slack-generated webhook URL
//     into the Notifications settings; that's the entire setup.
//   - Covers 90% of SeDoc notification use cases (workflow
//     assigned, share-link viewed, approval required).
//   - Full Slack app (OAuth + bot scopes + slash commands) is a
//     separate adapter when we need interactivity (approve/reject
//     from Slack). That's E8's follow-up.
//
// Security:
//   - URL contains a per-channel secret; treat it like a bearer token.
//     Never log the URL (only log whether Enabled()).
//   - POST bodies are unsigned by Slack's design; callers must
//     reach Enabled()==false if the tenant hasn't configured a URL.

// SlackConfig carries the outgoing-webhook URL and a sensible default
// username + icon. Per-tenant URLs come from the DB, not here — this
// struct only holds the process-wide defaults injected at boot.
type SlackConfig struct {
	// WebhookURL: if non-empty, used as the fallback for tenants that
	// haven't configured their own. Leave empty in multi-tenant prod
	// so every tenant must BYOI (bring-your-own-integration).
	WebhookURL string
	Username   string
	IconEmoji  string
	Timeout    time.Duration
}

// SlackSender matches the SMTPSender shape so the service layer can
// treat channels uniformly.
type SlackSender interface {
	Send(ctx context.Context, webhookURL, title, body string) error
	Enabled(webhookURL string) bool
}

// slackSender is the HTTP-backed implementation.
type slackSender struct {
	cfg  SlackConfig
	http *http.Client
}

// NewSlackSender constructs a sender. A 5s timeout is applied when
// cfg.Timeout is zero — Slack's webhook SLA is ~1s so 5s is
// generous without blocking the notification worker indefinitely.
func NewSlackSender(cfg SlackConfig) SlackSender {
	t := cfg.Timeout
	if t <= 0 {
		t = 5 * time.Second
	}
	return &slackSender{
		cfg:  cfg,
		http: &http.Client{Timeout: t},
	}
}

// Enabled reports whether a Send would attempt network I/O. The
// per-call webhookURL takes precedence over the process default —
// callers pass the tenant's stored URL (empty string when unset)
// and Enabled tells them whether sending will do anything.
func (s *slackSender) Enabled(webhookURL string) bool {
	return webhookURL != "" || s.cfg.WebhookURL != ""
}

// Send posts a message to Slack's incoming-webhook endpoint. Returns
// nil on success; non-nil on any HTTP or Slack-side failure. A
// non-2xx response body is included in the error for diagnostics
// (never the URL).
func (s *slackSender) Send(ctx context.Context, webhookURL, title, body string) error {
	url := webhookURL
	if url == "" {
		url = s.cfg.WebhookURL
	}
	if url == "" {
		return nil
	}
	if strings.TrimSpace(title) == "" && strings.TrimSpace(body) == "" {
		return fmt.Errorf("slack: title or body required")
	}

	payload := map[string]any{
		"text": slackTextFrom(title, body),
	}
	if s.cfg.Username != "" {
		payload["username"] = s.cfg.Username
	}
	if s.cfg.IconEmoji != "" {
		payload["icon_emoji"] = s.cfg.IconEmoji
	}
	raw, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("slack: build req: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("slack: post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		// Truncate to avoid polluting logs with large HTML error pages
		// Slack sometimes returns for revoked webhooks.
		if len(body) > 256 {
			body = body[:256]
		}
		return fmt.Errorf("slack: status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// slackTextFrom composes a single `text` field from the notification's
// title + body. Slack rendering is mrkdwn by default; bolding the
// title makes the preview line useful even when the body is long.
func slackTextFrom(title, body string) string {
	if title == "" {
		return body
	}
	if body == "" {
		return "*" + title + "*"
	}
	return "*" + title + "*\n" + body
}
