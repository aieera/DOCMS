package notifications

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"
)

func expoClientFor(t *testing.T, srv *httptest.Server) *ExpoClient {
	t.Helper()
	t.Setenv(envExpoPushURL, srv.URL)
	return NewExpoClient(zerolog.Nop())
}

func TestExpoSend_OKAndErrorTickets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msgs []ExpoMessage
		if err := json.NewDecoder(r.Body).Decode(&msgs); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(msgs) != 2 {
			t.Fatalf("want 2 msgs, got %d", len(msgs))
		}
		if msgs[0].To != "ExponentPushToken[aaa]" || msgs[0].Title != "T" {
			t.Fatalf("unexpected first message %+v", msgs[0])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"status":"ok","id":"tick-1"},
			{"status":"error","message":"gone","details":{"error":"DeviceNotRegistered"}}
		]}`))
	}))
	defer srv.Close()

	c := expoClientFor(t, srv)
	tickets, err := c.Send(t.Context(), []ExpoMessage{
		{To: "ExponentPushToken[aaa]", Title: "T", Body: "B"},
		{To: "ExponentPushToken[bbb]", Title: "T", Body: "B"},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(tickets) != 2 {
		t.Fatalf("want 2 tickets, got %d", len(tickets))
	}
	if !tickets[0].OK() || tickets[0].DeviceNotRegistered() {
		t.Fatalf("first ticket should be ok: %+v", tickets[0])
	}
	if tickets[1].OK() || !tickets[1].DeviceNotRegistered() {
		t.Fatalf("second ticket should be DeviceNotRegistered: %+v", tickets[1])
	}
}

func TestExpoSend_BatchesOver100(t *testing.T) {
	var batchSizes []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msgs []ExpoMessage
		_ = json.NewDecoder(r.Body).Decode(&msgs)
		batchSizes = append(batchSizes, len(msgs))
		tickets := make([]ExpoTicket, len(msgs))
		for i := range tickets {
			tickets[i] = ExpoTicket{Status: "ok"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": tickets})
	}))
	defer srv.Close()

	c := expoClientFor(t, srv)
	msgs := make([]ExpoMessage, 250)
	for i := range msgs {
		msgs[i] = ExpoMessage{To: "ExponentPushToken[x]"}
	}
	tickets, err := c.Send(t.Context(), msgs)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(tickets) != 250 {
		t.Fatalf("want 250 tickets, got %d", len(tickets))
	}
	want := []int{100, 100, 50}
	if len(batchSizes) != 3 || batchSizes[0] != want[0] || batchSizes[1] != want[1] || batchSizes[2] != want[2] {
		t.Fatalf("batch sizes = %v, want %v", batchSizes, want)
	}
}

func TestExpoSend_Non2xxErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()
	c := expoClientFor(t, srv)
	if _, err := c.Send(t.Context(), []ExpoMessage{{To: "t"}}); err == nil {
		t.Fatal("expected error on 502")
	}
}

func TestExpoSend_TicketCountMismatchErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"status":"ok"}]}`)) // 1 ticket for 2 msgs
	}))
	defer srv.Close()
	c := expoClientFor(t, srv)
	if _, err := c.Send(t.Context(), []ExpoMessage{{To: "a"}, {To: "b"}}); err == nil {
		t.Fatal("expected error on ticket/message count mismatch")
	}
}

func TestExpoSend_EmptyIsNoop(t *testing.T) {
	// No server: zero messages must make zero requests.
	t.Setenv(envExpoPushURL, "http://127.0.0.1:1") // would refuse if dialed
	c := NewExpoClient(zerolog.Nop())
	tickets, err := c.Send(t.Context(), nil)
	if err != nil || len(tickets) != 0 {
		t.Fatalf("empty send should noop: %v %v", tickets, err)
	}
}
