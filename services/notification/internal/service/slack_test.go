package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSlack_Enabled(t *testing.T) {
	s := NewSlackSender(SlackConfig{})
	if s.Enabled("") {
		t.Error("no URL → disabled")
	}
	if !s.Enabled("https://hooks.slack.com/services/XXX") {
		t.Error("per-call URL → enabled")
	}
	s2 := NewSlackSender(SlackConfig{WebhookURL: "https://hooks.slack.com/services/DEFAULT"})
	if !s2.Enabled("") {
		t.Error("default URL → enabled when per-call is empty")
	}
}

func TestSlack_Send_HappyPath(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	s := NewSlackSender(SlackConfig{Username: "vaultdms", IconEmoji: ":page:"})
	err := s.Send(context.Background(), srv.URL, "Doc approved", "Contract-2026-04 is now active.")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text, ok := got["text"].(string); !ok || !strings.Contains(text, "*Doc approved*") {
		t.Errorf("title should be bolded in text: %v", got["text"])
	}
	if got["username"] != "vaultdms" {
		t.Errorf("username not propagated: %v", got["username"])
	}
}

func TestSlack_Send_EmptyWhenNoURL_IsNoOp(t *testing.T) {
	// No test server; if the sender tried to hit the network, DNS
	// would fail. Empty URL + empty default should silently succeed.
	s := NewSlackSender(SlackConfig{})
	if err := s.Send(context.Background(), "", "t", "b"); err != nil {
		t.Errorf("no-URL send must be a no-op, got: %v", err)
	}
}

func TestSlack_Send_Non2xx_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no_service", 404)
	}))
	defer srv.Close()

	s := NewSlackSender(SlackConfig{})
	err := s.Send(context.Background(), srv.URL, "t", "b")
	if err == nil {
		t.Fatal("want non-nil error")
	}
	// The URL must NOT appear in the error — treat it like a bearer
	// token. Only the status + body leak.
	if strings.Contains(err.Error(), srv.URL) {
		t.Errorf("error message leaks the webhook URL: %v", err)
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should include status code: %v", err)
	}
}

func TestSlack_Send_RejectsEmptyTitleAndBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("should not have been called")
	}))
	defer srv.Close()
	s := NewSlackSender(SlackConfig{})
	err := s.Send(context.Background(), srv.URL, "", "")
	if err == nil {
		t.Fatal("empty title+body must error")
	}
}

func TestSlackTextFrom(t *testing.T) {
	cases := []struct{ title, body, want string }{
		{"T", "B", "*T*\nB"},
		{"T", "", "*T*"},
		{"", "B", "B"},
	}
	for _, tc := range cases {
		if got := slackTextFrom(tc.title, tc.body); got != tc.want {
			t.Errorf("slackTextFrom(%q,%q)=%q, want %q", tc.title, tc.body, got, tc.want)
		}
	}
}

// Compile-check the truncation branch without a real long body.
func TestSlack_TruncatesLongErrorBody(t *testing.T) {
	big := strings.Repeat("x", 1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = io.WriteString(w, big)
	}))
	defer srv.Close()
	s := NewSlackSender(SlackConfig{})
	err := s.Send(context.Background(), srv.URL, "t", "b")
	if err == nil {
		t.Fatal("want error")
	}
	if len(err.Error()) > 400 {
		t.Errorf("error should be truncated, len=%d", len(err.Error()))
	}
}
