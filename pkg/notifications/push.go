package notifications

import (
	"context"
	"errors"
)

// Push is the *backend surface* for FCM/APNs MFA pushes. The auth
// service does not talk to Google or Apple directly — that's the
// notification service's job once Phase 11.3 (mobile app) ships.
//
// What this package owns:
//   - the device-registration data shape (PushDevice)
//   - the challenge envelope (PushChallenge)
//   - a Sender interface + a NoopSender that emits no actual push.
//     The auth service emits a `dms.auth.mfa_push_challenged.v1`
//     NATS event in lieu of calling here when Sender == nil.
//
// Concrete FCM / APNs adapters land in pkg/notifications/push_fcm.go
// and push_apns.go alongside the mobile work.

// PushPlatform — narrow type so callers can't pass "android".
type PushPlatform string

const (
	PlatformFCM  PushPlatform = "fcm"
	PlatformAPNs PushPlatform = "apns"
)

// PushDevice is the registration payload the mobile app submits via
// POST /auth/mfa/push/devices. Token is opaque; AckKey is generated
// server-side, sealed with the per-tenant KEK, and returned to the
// device once on registration.
type PushDevice struct {
	DeviceID string
	Platform PushPlatform
	Token    string
	Label    string
}

// PushChallenge is what the auth service hands the mobile app to
// approve a sign-in. The device verifies it received the challenge
// and signs the ack with its per-device HMAC key.
type PushChallenge struct {
	ChallengeID string
	UserID      string
	TenantID    string
	IssuedAt    int64
	ExpiresAt   int64
	IPAddress   string
	UserAgent   string
}

// Sender is the dispatcher interface. Implementations:
//   - NoopSender (dev / tests): no I/O.
//   - NATSEventSender (default): emits dms.auth.mfa_push_challenged.v1
//     so the notification service or future mobile worker can send
//     the actual FCM / APNs payload.
//
// Direct FCM / APNs adapters arrive in Phase 11.3. The interface
// stays the same; only the wiring changes.
type Sender interface {
	Dispatch(ctx context.Context, dev *PushDevice, ch *PushChallenge) error
}

// NoopSender swallows every dispatch. Returned from NewSender when
// the deploy hasn't wired anything; the auth service surfaces the
// push *method* as unavailable in the login picker.
type NoopSender struct{}

// Dispatch — no-op.
func (NoopSender) Dispatch(_ context.Context, _ *PushDevice, _ *PushChallenge) error {
	return ErrPushNotImplemented
}

// ErrPushNotImplemented surfaces from NoopSender. The service layer
// hides the push method on the login picker when this is what the
// dispatcher returns at startup.
var ErrPushNotImplemented = errors.New("push: backend dispatcher not configured")
