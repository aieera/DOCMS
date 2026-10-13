package email

import (
	"bufio"
	"context"
	"encoding/base64"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// N-5: an IMAP config is an outbound connection the tenant chooses, so it
// gets the webhook SSRF treatment: no internal hosts, IMAP ports only.
func TestIMAPConfig_PrivateHostRejected(t *testing.T) {
	// A real key so password sealing succeeds and the host check is what
	// decides.
	t.Setenv("SEDOC_LOCAL_KEK", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	s := New(nil, nil, nil, nil, zerolog.Nop())
	for _, host := range []string{
		"127.0.0.1", "localhost", "10.0.0.5", "192.168.1.10", "172.16.0.1",
		"169.254.169.254", "100.64.0.1", "::1", "fd00::1", "0.0.0.0",
	} {
		// pool is nil: a host that passed validation panics on the insert.
		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					err = nil
					t.Errorf("CreateConfig accepted internal IMAP host %q (reached the DB)", host)
				}
			}()
			_, err = s.CreateConfig(context.Background(), "6f1c1b7e-8a8e-4a43-9a1e-0f7a3f9d2c11", "u1", CreateConfigInput{
				Source: SourceIMAP, Label: "x", IMAPHost: host, IMAPPort: 993, IMAPUseTLS: true,
				IMAPUsername: "u", IMAPPassword: "p",
			})
			if err == nil {
				t.Errorf("CreateConfig accepted internal IMAP host %q", host)
			}
		}()
	}
	// Non-IMAP ports are refused even for a public address (port-scan oracle).
	for _, port := range []int{22, 25, 80, 5432, 6379} {
		if err := validateIMAPEndpoint(context.Background(), "93.184.216.34", port, false); err == nil {
			t.Errorf("port %d accepted", port)
		}
	}
	if err := validateIMAPEndpoint(context.Background(), "93.184.216.34", 993, false); err != nil {
		t.Errorf("public host on 993 rejected: %v", err)
	}
	// On-prem opt-in relaxes the guard.
	if err := validateIMAPEndpoint(context.Background(), "10.0.0.5", 1143, true); err != nil {
		t.Errorf("allowPrivate should accept a LAN mail server: %v", err)
	}
}

// fakeIMAP is a plain-TCP IMAP server without STARTTLS that records every
// command line it receives.
type fakeIMAP struct {
	ln    net.Listener
	mu    sync.Mutex
	lines []string
	conns int
}

func startFakeIMAP(t *testing.T) *fakeIMAP {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIMAP{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.conns++
			f.mu.Unlock()
			go f.serve(c)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return f
}

func (f *fakeIMAP) serve(c net.Conn) {
	defer c.Close()
	_, _ = c.Write([]byte("* OK [CAPABILITY IMAP4rev1 AUTH=PLAIN] fake ready\r\n"))
	r := bufio.NewReader(c)
	for {
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		f.mu.Lock()
		f.lines = append(f.lines, line)
		f.mu.Unlock()
		tag, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
		switch strings.ToUpper(strings.Fields(rest + " x")[0]) {
		case "CAPABILITY":
			_, _ = c.Write([]byte("* CAPABILITY IMAP4rev1 AUTH=PLAIN\r\n" + tag + " OK done\r\n"))
		case "STARTTLS":
			_, _ = c.Write([]byte(tag + " BAD STARTTLS not supported\r\n"))
		case "LOGOUT":
			_, _ = c.Write([]byte("* BYE\r\n" + tag + " OK bye\r\n"))
			return
		default:
			_, _ = c.Write([]byte(tag + " NO nope\r\n"))
		}
	}
}

func (f *fakeIMAP) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

func (f *fakeIMAP) saw(substr string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, l := range f.lines {
		if strings.Contains(l, substr) {
			return true
		}
	}
	return false
}

func TestIMAPDial_NoCleartextLogin(t *testing.T) {
	srv := startFakeIMAP(t)
	b := &IMAPBackend{Log: zerolog.Nop(), AllowPrivate: true}
	cfg := &Config{ID: "c1", TenantID: "t1", IMAPHost: "127.0.0.1", IMAPPort: srv.port(), IMAPUseTLS: false, IMAPUsername: "alice"}
	_, err := b.fetchWithPassword(context.Background(), cfg, "hunter2")
	if err == nil {
		t.Fatal("session without TLS succeeded")
	}
	time.Sleep(50 * time.Millisecond)
	if srv.saw("hunter2") || srv.saw("LOGIN") || srv.saw("AUTHENTICATE") {
		t.Fatalf("credentials sent in cleartext: %q", srv.lines)
	}
	if !srv.saw("STARTTLS") {
		t.Fatalf("imap_use_tls=false must negotiate STARTTLS, server saw %q", srv.lines)
	}
	// Callers get a generic error, not connection details.
	if strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), strconv.Itoa(srv.port())) {
		t.Errorf("error leaks endpoint details: %v", err)
	}
}

func TestIMAPDial_PrivateBlockedAtDial(t *testing.T) {
	srv := startFakeIMAP(t)
	b := &IMAPBackend{Log: zerolog.Nop()} // AllowPrivate false
	cfg := &Config{ID: "c1", TenantID: "t1", IMAPHost: "127.0.0.1", IMAPPort: srv.port(), IMAPUseTLS: true, IMAPUsername: "alice"}
	if _, err := b.fetchWithPassword(context.Background(), cfg, "hunter2"); err == nil {
		t.Fatal("dial to loopback succeeded")
	}
	time.Sleep(50 * time.Millisecond)
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.conns != 0 {
		t.Fatalf("connection to an internal address was opened (%d)", srv.conns)
	}
}
