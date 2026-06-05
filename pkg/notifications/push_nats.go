package notifications

import (
	"context"
	"encoding/json"

	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
)

// PushChallengeSubject is the real-time NATS subject the auth service emits an
// MFA push challenge on. The notification service / mobile-push worker
// subscribes and delivers the actual FCM/APNs payload (Phase 11.3). It's a core
// (non-JetStream) publish on purpose: the challenge is time-sensitive (≤2 min
// Redis TTL) and best-effort — the first device to ack wins — so durability
// isn't worth the outbox round-trip.
const PushChallengeSubject = "dms.auth.mfa_push_challenged.v1"

// PushChallengeEvent is the wire payload: enough for a downstream worker to
// build + deliver the FCM/APNs push and show the approval prompt.
type PushChallengeEvent struct {
	Device    *PushDevice    `json:"device"`
	Challenge *PushChallenge `json:"challenge"`
}

// NATSEventSender is the default dispatcher (ADR / Phase 11.3): rather than
// talk to Google/Apple directly, the auth service emits a challenge event and
// the notification service / mobile worker delivers it. Decouples auth from the
// push-provider identity.
type NATSEventSender struct {
	nc  *nats.Conn
	log zerolog.Logger
}

// NewNATSEventSender constructs the sender. nc must be a live connection.
func NewNATSEventSender(nc *nats.Conn, log zerolog.Logger) *NATSEventSender {
	return &NATSEventSender{nc: nc, log: log}
}

// Dispatch publishes the challenge event. A publish failure is returned so the
// caller can fall back; a nil nc is a programming error guarded at construction.
func (s *NATSEventSender) Dispatch(_ context.Context, dev *PushDevice, ch *PushChallenge) error {
	payload, err := json.Marshal(PushChallengeEvent{Device: dev, Challenge: ch})
	if err != nil {
		return err
	}
	if err := s.nc.Publish(PushChallengeSubject, payload); err != nil {
		return err
	}
	s.log.Debug().
		Str("challenge_id", ch.ChallengeID).
		Str("device_id", dev.DeviceID).
		Str("subject", PushChallengeSubject).
		Msg("mfa push challenge emitted")
	return nil
}
