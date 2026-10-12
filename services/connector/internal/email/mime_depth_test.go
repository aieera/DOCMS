package email

import (
	"fmt"
	"net/mail"
	"strings"
	"testing"
	"time"
)

// nestedMessage builds a message whose body is `depth` multiparts nested
// inside each other with a text leaf at the bottom.
func nestedMessage(depth int) string {
	var b strings.Builder
	b.WriteString("From: a@example.test\r\nSubject: nest\r\n")
	b.WriteString("Content-Type: multipart/mixed; boundary=\"b0\"\r\n\r\n")
	for i := 0; i < depth; i++ {
		fmt.Fprintf(&b, "--b%d\r\n", i)
		if i == depth-1 {
			b.WriteString("Content-Type: text/plain\r\n\r\nleaf body\r\n")
		} else {
			fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=\"b%d\"\r\n\r\n", i+1)
		}
	}
	for i := depth - 1; i >= 0; i-- {
		fmt.Fprintf(&b, "--b%d--\r\n", i)
	}
	return b.String()
}

// N-9: MIME nesting is bounded so a hostile message cannot drive unbounded
// recursion; a sane depth still parses.
func TestMIME_DepthBounded(t *testing.T) {
	ok, err := mail.ReadMessage(strings.NewReader(nestedMessage(5)))
	if err != nil {
		t.Fatal(err)
	}
	body, _, err := walkMIME(ok)
	if err != nil || !strings.Contains(body, "leaf body") {
		t.Fatalf("depth 5: body=%q err=%v", body, err)
	}

	deep, err := mail.ReadMessage(strings.NewReader(nestedMessage(5000)))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, _, err := walkMIME(deep); err == nil {
		t.Fatal("depth 5000 accepted; want a nesting error")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("deep message took too long to reject")
	}
}

// Too many attachments in one message is refused rather than filing
// thousands of documents.
func TestMIME_AttachmentCountBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString("Subject: many\r\nContent-Type: multipart/mixed; boundary=\"x\"\r\n\r\n")
	for i := 0; i < maxAttachmentsPerMessage+1; i++ {
		fmt.Fprintf(&b, "--x\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=\"f%d.bin\"\r\n\r\ndata\r\n", i)
	}
	b.WriteString("--x--\r\n")
	msg, err := mail.ReadMessage(strings.NewReader(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := walkMIME(msg); err == nil {
		t.Fatal("attachment count over the cap accepted")
	}
}
