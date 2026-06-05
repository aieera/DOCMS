package notifications

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
)

func sampleChallenge() (*PushDevice, *PushChallenge) {
	dev := &PushDevice{DeviceID: "dev-1", Platform: PlatformFCM, Token: "tok-abcdefghij", Label: "Pixel"}
	ch := &PushChallenge{ChallengeID: "ch-1", UserID: "u-1", TenantID: "t-1", ExpiresAt: time.Now().Add(2 * time.Minute).Unix()}
	return dev, ch
}

// TestLogSenderDispatches: LogSender reports success (nil), unlike NoopSender.
func TestLogSenderDispatches(t *testing.T) {
	dev, ch := sampleChallenge()
	if err := NewLogSender(zerolog.Nop()).Dispatch(context.Background(), dev, ch); err != nil {
		t.Fatalf("LogSender.Dispatch: %v", err)
	}
	// NoopSender, by contrast, signals unavailability.
	if err := (NoopSender{}).Dispatch(context.Background(), dev, ch); err == nil {
		t.Fatal("NoopSender should return ErrPushNotImplemented")
	}
}

// TestFactorySelects: NewSender maps SEDOC_PUSH_SENDER to the right type.
func TestFactorySelects(t *testing.T) {
	log := zerolog.Nop()
	if _, ok := NewSender("log", nil, log).(*LogSender); !ok {
		t.Error("log → LogSender")
	}
	if _, ok := NewSender("noop", nil, log).(NoopSender); !ok {
		t.Error("noop → NoopSender")
	}
	// nats without a connection falls back to noop.
	if _, ok := NewSender("nats", nil, log).(NoopSender); !ok {
		t.Error("nats+nil → NoopSender fallback")
	}
	// unknown → noop.
	if _, ok := NewSender("bogus", nil, log).(NoopSender); !ok {
		t.Error("unknown → NoopSender")
	}
}

// TestNATSEventSenderPublishes: the default sender emits the challenge event on
// the documented subject, with the device + challenge in the payload. Skipped
// unless NATS_TEST_URL points at a broker.
func TestNATSEventSenderPublishes(t *testing.T) {
	url := os.Getenv("NATS_TEST_URL")
	if url == "" {
		t.Skip("no NATS_TEST_URL; skipping live NATS publish test")
	}
	nc, err := nats.Connect(url)
	if err != nil {
		t.Skipf("nats connect: %v", err)
	}
	defer nc.Close()

	sub, err := nc.SubscribeSync(PushChallengeSubject)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	dev, ch := sampleChallenge()
	if err := NewNATSEventSender(nc, zerolog.Nop()).Dispatch(context.Background(), dev, ch); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	msg, err := sub.NextMsg(2 * time.Second)
	if err != nil {
		t.Fatalf("no event received: %v", err)
	}
	var ev PushChallengeEvent
	if err := json.Unmarshal(msg.Data, &ev); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if ev.Challenge == nil || ev.Challenge.ChallengeID != "ch-1" || ev.Device == nil || ev.Device.DeviceID != "dev-1" {
		t.Fatalf("unexpected payload: %+v", ev)
	}
}
