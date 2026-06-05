package notifications

import (
	"context"

	"github.com/rs/zerolog"
)

// LogSender is a dev/observability dispatcher: it records every push challenge
// to the log instead of delivering it. Unlike NoopSender it returns nil (the
// dispatch "succeeded"), so the auth service surfaces push as an available MFA
// method and the challenge is visible to anyone watching the logs — useful for
// local dev + integration tests before the mobile app + a real backend exist.
type LogSender struct {
	log zerolog.Logger
}

// NewLogSender constructs a LogSender.
func NewLogSender(log zerolog.Logger) *LogSender { return &LogSender{log: log} }

// Dispatch logs the challenge. PII (the device token) is truncated.
func (s *LogSender) Dispatch(_ context.Context, dev *PushDevice, ch *PushChallenge) error {
	tok := dev.Token
	if len(tok) > 8 {
		tok = tok[:8] + "…"
	}
	s.log.Info().
		Str("challenge_id", ch.ChallengeID).
		Str("user_id", ch.UserID).
		Str("device_id", dev.DeviceID).
		Str("platform", string(dev.Platform)).
		Str("token", tok).
		Int64("expires_at", ch.ExpiresAt).
		Msg("mfa push challenge (log-mode dispatcher; no real push sent)")
	return nil
}
