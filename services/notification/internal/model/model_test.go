package model

import "testing"

// Channel constants are written into the notifications table and consumed by
// downstream systems (email worker, slack bot, sms gateway). Pin the exact
// string values so a rename shows up as a test failure, not as silently
// mis-routed notifications.

func TestChannelConstants_AreStableStrings(t *testing.T) {
	cases := map[Channel]string{
		ChannelInApp: "in_app",
		ChannelEmail: "email",
		ChannelSlack: "slack",
		ChannelTeams: "teams",
		ChannelPush:  "push",
		ChannelSMS:   "sms",
	}
	for c, want := range cases {
		if string(c) != want {
			t.Errorf("channel %q: got %q, want %q", want, string(c), want)
		}
	}
}

func TestUserPreference_DefaultsZero(t *testing.T) {
	// Zero-value preference means "no email, no push, no slack, no sms,
	// no quiet hours". The Deliver path relies on this to keep dev users
	// notification-free until they explicitly enable a channel.
	var p UserPreference
	if p.EmailEnabled || p.PushEnabled || p.SlackEnabled || p.SMSEnabled {
		t.Errorf("zero-value UserPreference should have all channels off: %+v", p)
	}
	if p.QuietHoursFrom != 0 || p.QuietHoursTo != 0 {
		t.Errorf("zero-value quiet hours should be 0: %+v", p)
	}
}

func TestDeliveryPayload_RequiredFields(t *testing.T) {
	// Consumer (nats_consumer._on_ocr_completed and friends) checks
	// TenantID and UserIDs non-empty before invoking Deliver. This test
	// documents that contract.
	p := DeliveryPayload{}
	if p.TenantID != "" || len(p.UserIDs) != 0 {
		t.Error("zero DeliveryPayload must look empty")
	}
}
