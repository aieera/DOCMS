package service

import (
	"bufio"
	"net"
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

func TestSMTPSender_DialsIPv6HostPort(t *testing.T) {
	// An IPv6 Host must be bracketed in the dial address
	// ("[::1]:25", not "::1:25") or the dial fails before any
	// SMTP traffic happens.
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	defer ln.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		// Greet, wait for the client's first command, then hang up
		// so Send fails deterministically after the dial phase.
		_, _ = conn.Write([]byte("220 test ESMTP\r\n"))
		_, _ = bufio.NewReader(conn).ReadString('\n')
		_ = conn.Close()
	}()

	port := ln.Addr().(*net.TCPAddr).Port
	s := NewSMTPSender(SMTPConfig{Host: "::1", Port: port, From: "from@a"})
	err = s.Send("to@b", "Subject", "Body")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "smtp dial",
		"dial must succeed for an IPv6 host; got %v", err)
	<-done
}

func TestContainsAt(t *testing.T) {
	require.True(t, containsAt("a@b"))
	require.False(t, containsAt("uuid-only"))
	require.False(t, containsAt(""))
}
