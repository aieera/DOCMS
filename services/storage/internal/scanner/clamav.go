// Package scanner is a lightweight ClamAV clamd INSTREAM client.
// Protocol reference: https://linux.die.net/man/8/clamd (INSTREAM section).
//
// The client is stateless and goroutine-safe: each Scan opens its own TCP
// connection, streams the bytes, and reads the response. Timeouts are
// enforced per-call via the caller's context.
package scanner

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
)

// Result captures the outcome of one scan.
type Result struct {
	Infected  bool
	Signature string // virus name on hit; empty on clean
	Raw       string // full clamd response line, for logs
}

// Client talks to a clamd instance over TCP.
type Client struct {
	addr    string
	timeout time.Duration
}

// New constructs a client. `addr` is host:port (typically "clamav:3310");
// timeout bounds the whole scan (connect + transfer + response).
func New(addr string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &Client{addr: addr, timeout: timeout}
}

// Ping sends "zPING\0" and verifies the server responds "PONG". Used by
// health checks. Short 2s default timeout independent of the scan timeout.
func (c *Client) Ping(ctx context.Context) error {
	conn, err := c.dial(ctx, 2*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("zPING\x00")); err != nil {
		return fmt.Errorf("clamav ping write: %w", err)
	}
	buf := make([]byte, 16)
	n, err := conn.Read(buf)
	if err != nil {
		return fmt.Errorf("clamav ping read: %w", err)
	}
	resp := strings.TrimRight(string(buf[:n]), "\x00 \r\n")
	if resp != "PONG" {
		return fmt.Errorf("clamav ping unexpected: %q", resp)
	}
	return nil
}

// Scan streams `r` through clamd and returns the result. Per the INSTREAM
// protocol, each chunk is prefixed with a 4-byte big-endian length; an
// empty chunk (length 0) terminates the stream.
func (c *Client) Scan(ctx context.Context, r io.Reader) (*Result, error) {
	conn, err := c.dial(ctx, c.timeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	// Command prefix. Using zINSTREAM (null-terminated) matches modern
	// clamd defaults; nINSTREAM is legacy newline-terminated.
	if _, err := conn.Write([]byte("zINSTREAM\x00")); err != nil {
		return nil, fmt.Errorf("clamav instream cmd: %w", err)
	}

	// Stream chunks. 64 KiB is a sweet spot: large enough to minimize
	// per-chunk overhead, small enough not to dominate a pause on a slow
	// disk read.
	const chunkSize = 64 * 1024
	buf := make([]byte, chunkSize)
	for {
		n, readErr := r.Read(buf)
		if n > 0 {
			var sz [4]byte
			binary.BigEndian.PutUint32(sz[:], uint32(n))
			if _, err := conn.Write(sz[:]); err != nil {
				return nil, fmt.Errorf("clamav chunk length: %w", err)
			}
			if _, err := conn.Write(buf[:n]); err != nil {
				return nil, fmt.Errorf("clamav chunk data: %w", err)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("source read: %w", readErr)
		}
	}
	// Terminator: 4-byte length 0.
	if _, err := conn.Write([]byte{0, 0, 0, 0}); err != nil {
		return nil, fmt.Errorf("clamav terminator: %w", err)
	}

	// Read the entire response until the connection closes. clamd sends a
	// single line like "stream: OK" or "stream: Eicar-Test-Signature FOUND".
	raw, err := io.ReadAll(conn)
	if err != nil {
		return nil, fmt.Errorf("clamav read response: %w", err)
	}
	resp := strings.TrimRight(string(raw), "\x00 \r\n")
	return parse(resp)
}

func parse(resp string) (*Result, error) {
	// Errors: "stream: ERROR: ...", "INSTREAM size limit exceeded. ERROR", etc.
	if strings.Contains(resp, "ERROR") {
		return nil, fmt.Errorf("clamav error: %s", resp)
	}
	res := &Result{Raw: resp}
	if strings.HasSuffix(resp, " OK") {
		return res, nil
	}
	// Format: "stream: <Signature> FOUND"
	if idx := strings.LastIndex(resp, " FOUND"); idx > 0 {
		prefix := resp[:idx]
		if c := strings.IndexByte(prefix, ':'); c > 0 {
			res.Signature = strings.TrimSpace(prefix[c+1:])
		}
		res.Infected = true
		return res, nil
	}
	return res, vdmserr.Internal("clamav unparseable response: " + resp)
}

func (c *Client) dial(ctx context.Context, timeout time.Duration) (net.Conn, error) {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return nil, fmt.Errorf("clamav dial: %w", err)
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))
	return conn, nil
}
