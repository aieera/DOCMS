package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSMTPSender_DisabledWhenHostEmpty(t *testing.T) {
	s := NewSMTPSender(SMTPConfig{})
	require.False(t, s.Enabled())
	// Send is a no-op when disabled — returns nil, no I/O.
	require.NoError(t, s.Send("alice@example.com", "Subject", "Body"))
}

func TestSMTPSender_RejectsEmptyTo(t *testing.T) {
	s := NewSMTPSender(SMTPConfig{Host: "localhost", Port: 25})
	err := s.Send("", "Subject", "Body")
	require.Error(t, err)
	require.Contains(t, err.Error(), "to required")
}

func TestBuildRFC5322_IncludesHeaders(t *testing.T) {
	msg := buildRFC5322("from@a", "to@b", "hi", "body\nline2")
	// Every header must be CRLF terminated (RFC 5322 §2.1).
	require.Contains(t, msg, "From: from@a\r\n")
	require.Contains(t, msg, "To: to@b\r\n")
	require.Contains(t, msg, "Subject: hi\r\n")
	require.Contains(t, msg, "MIME-Version: 1.0\r\n")
	require.Contains(t, msg, "Content-Type: text/plain; charset=UTF-8\r\n")
	// Blank line separates headers from body.
	require.True(t, strings.Contains(msg, "\r\n\r\nbody"),
		"header/body separator missing")
	require.True(t, strings.HasSuffix(msg, "body\nline2"),
		"body must be appended verbatim")
}

func TestContainsAt(t *testing.T) {
	require.True(t, containsAt("a@b"))
	require.False(t, containsAt("uuid-only"))
	require.False(t, containsAt(""))
}
