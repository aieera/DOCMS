// Expo Push transport (ADR 0117 — mobile app).
//
// The mobile app is an Expo (React Native) client; Expo's push service
// fronts both FCM and APNs behind one HTTPS endpoint, so the backend
// needs no Google/Apple credentials — it POSTs batches of messages to
// https://exp.host/--/api/v2/push/send addressed by the per-device
// ExponentPushToken the app registers via the notification service's
// device API. This is the first concrete delivery adapter for the
// "adapters land with the mobile work" deferral documented in push.go;
// native push_fcm.go / push_apns.go remain future options behind the
// same call shape.
//
// Batching: Expo caps a request at 100 messages. Send splits larger
// slices transparently and returns one ExpoTicket per message, in
// order. A ticket with Error == "DeviceNotRegistered" means the token
// is dead (app uninstalled / token rotated) — callers should revoke
// that device row so the fleet self-heals.
package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/rs/zerolog"
)

// envExpoPushURL overrides the Expo endpoint (tests, self-hosted proxy).
const envExpoPushURL = "SEDOC_EXPO_PUSH_URL"

const defaultExpoPushURL = "https://exp.host/--/api/v2/push/send"

// expoBatchMax is Expo's documented per-request message cap.
const expoBatchMax = 100

// ExpoMessage is one push notification to one Expo push token.
type ExpoMessage struct {
	To    string            `json:"to"`
	Title string            `json:"title,omitempty"`
	Body  string            `json:"body,omitempty"`
	Data  map[string]string `json:"data,omitempty"`
	Sound string            `json:"sound,omitempty"`
}

// ExpoTicket is the per-message receipt from the push service.
type ExpoTicket struct {
	Status  string `json:"status"` // "ok" | "error"
	ID      string `json:"id,omitempty"`
	Message string `json:"message,omitempty"`
	Details struct {
		Error string `json:"error,omitempty"`
	} `json:"details,omitempty"`
}

// OK reports whether the message was accepted for delivery.
func (t ExpoTicket) OK() bool { return t.Status == "ok" }

// DeviceNotRegistered reports the token is permanently dead; the caller
// should revoke the device registration.
func (t ExpoTicket) DeviceNotRegistered() bool {
	return t.Details.Error == "DeviceNotRegistered"
}

// ExpoClient POSTs message batches to the Expo push service.
type ExpoClient struct {
	url string
	hc  *http.Client
	log zerolog.Logger
}

// NewExpoClient builds a client against SEDOC_EXPO_PUSH_URL or the
// public Expo endpoint.
func NewExpoClient(log zerolog.Logger) *ExpoClient {
	url := os.Getenv(envExpoPushURL)
	if url == "" {
		url = defaultExpoPushURL
	}
	return &ExpoClient{
		url: url,
		hc:  &http.Client{Timeout: 15 * time.Second},
		log: log,
	}
}

// Send delivers msgs (split into ≤100-message batches) and returns one
// ticket per message, positionally aligned with the input. A transport
// or non-2xx failure aborts the remaining batches and returns the
// tickets collected so far alongside the error.
func (c *ExpoClient) Send(ctx context.Context, msgs []ExpoMessage) ([]ExpoTicket, error) {
	tickets := make([]ExpoTicket, 0, len(msgs))
	for start := 0; start < len(msgs); start += expoBatchMax {
		end := start + expoBatchMax
		if end > len(msgs) {
			end = len(msgs)
		}
		batch, err := c.sendBatch(ctx, msgs[start:end])
		if err != nil {
			return tickets, err
		}
		tickets = append(tickets, batch...)
	}
	return tickets, nil
}

func (c *ExpoClient) sendBatch(ctx context.Context, msgs []ExpoMessage) ([]ExpoTicket, error) {
	payload, err := json.Marshal(msgs)
	if err != nil {
		return nil, fmt.Errorf("expo: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("expo: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("expo: post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("expo: status %d: %s", resp.StatusCode, snippetBytes(body))
	}
	var out struct {
		Data []ExpoTicket `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("expo: decode: %w", err)
	}
	if len(out.Data) != len(msgs) {
		// Positional alignment is part of the contract — bail rather
		// than mis-attribute tickets to the wrong devices.
		return nil, fmt.Errorf("expo: got %d tickets for %d messages", len(out.Data), len(msgs))
	}
	return out.Data, nil
}

func snippetBytes(b []byte) string {
	const max = 300
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "…"
}
