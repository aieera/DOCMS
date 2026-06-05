package notifications

import (
	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
)

// NewSender selects the MFA push dispatcher from SEDOC_PUSH_SENDER:
//
//	nats (default when NATS is up) — NATSEventSender: emit dms.auth
//	    .mfa_push_challenged.v1 for the notification service / mobile worker
//	    to deliver the real FCM/APNs push.
//	log  — LogSender: record challenges to the log (dev / integration tests).
//	noop — NoopSender: surface push as unavailable (no dispatch).
//
// Unknown values fall back to noop with a warning so a deploy typo doesn't
// silently send invalid pushes.
func NewSender(kind string, nc *nats.Conn, log zerolog.Logger) Sender {
	switch kind {
	case "nats":
		if nc != nil {
			return NewNATSEventSender(nc, log)
		}
		log.Warn().Msg("push: SEDOC_PUSH_SENDER=nats but no NATS connection; using noop")
		return NoopSender{}
	case "log":
		return NewLogSender(log)
	case "noop":
		return NoopSender{}
	case "":
		// Default: emit the challenge event when NATS is available (the
		// documented default — downstream delivers), else noop.
		if nc != nil {
			return NewNATSEventSender(nc, log)
		}
		return NoopSender{}
	default:
		log.Warn().Str("kind", kind).Msg("push: unknown SEDOC_PUSH_SENDER; using noop")
		return NoopSender{}
	}
}
