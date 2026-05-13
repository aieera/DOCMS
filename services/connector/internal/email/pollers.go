package email

import (
	"context"
	"errors"
)

// MicrosoftPoller wraps the existing providers/microsoft Connector so we
// don't duplicate OAuth + Graph plumbing. v1 returns an empty envelope
// list whenever an unconfigured provider is asked to poll — the worker
// records the no-op so the admin UI can see that polling ran.
type MicrosoftPoller struct{ MS pollerBackend }

// GmailPoller is the Gmail equivalent.
type GmailPoller struct{ Google pollerBackend }

// IMAPPoller is the IMAP fallback path. Implementation deliberately
// minimal in v1 — emersion/go-imap integration is wired but credentials
// + per-folder filtering land in Wave 12.5b. The poller returns an
// empty list with no error when no IMAP backend is configured so the
// worker doesn't spam the error column.
type IMAPPoller struct{ Backend pollerBackend }

// pollerBackend is the optional plug callers can supply to delegate the
// actual fetch. nil means "no-op poll" — useful for dev / CI where we
// don't want real network calls.
type pollerBackend interface {
	Fetch(ctx context.Context, cfg *Config) ([]*Envelope, error)
}

func (p *MicrosoftPoller) Source() Source { return SourceMicrosoft }
func (p *MicrosoftPoller) Poll(ctx context.Context, cfg *Config) ([]*Envelope, error) {
	if p.MS == nil {
		return nil, nil
	}
	if cfg.OAuthProvider == "" {
		return nil, errors.New("microsoft poller: oauth_provider not set")
	}
	return p.MS.Fetch(ctx, cfg)
}

func (p *GmailPoller) Source() Source { return SourceGmail }
func (p *GmailPoller) Poll(ctx context.Context, cfg *Config) ([]*Envelope, error) {
	if p.Google == nil {
		return nil, nil
	}
	if cfg.OAuthProvider == "" {
		return nil, errors.New("gmail poller: oauth_provider not set")
	}
	return p.Google.Fetch(ctx, cfg)
}

func (p *IMAPPoller) Source() Source { return SourceIMAP }
func (p *IMAPPoller) Poll(ctx context.Context, cfg *Config) ([]*Envelope, error) {
	if p.Backend == nil {
		return nil, nil
	}
	if cfg.IMAPHost == "" || cfg.IMAPUsername == "" {
		return nil, errors.New("imap poller: host/username required")
	}
	return p.Backend.Fetch(ctx, cfg)
}