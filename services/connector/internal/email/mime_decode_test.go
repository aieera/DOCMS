package email

import (
	"bytes"
	"net/mail"
	"strings"
	"testing"
)

// Regression: Go's multipart.Reader hands back part bodies VERBATIM — it
// does not apply Content-Transfer-Encoding. Reading a base64 attachment
// with io.ReadAll therefore stored the base64 TEXT as the file, so an
// ingested PDF began "JVBERi0x" (base64 of "%PDF-1") instead of "%PDF-".
// Both OCR ("file couldn't be read — corrupted or truncated") and the
// browser viewer ("Failed to load PDF document") rejected it.
func TestWalkMIME_DecodesBase64Attachment(t *testing.T) {
	// "%PDF-1.4\n%fake" base64-encoded, wrapped like a real MUA does.
	raw := "From: a@b.c\r\n" +
		"Subject: with attachment\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=BOUND\r\n\r\n" +
		"--BOUND\r\n" +
		"Content-Type: text/plain\r\n\r\n" +
		"see attached\r\n" +
		"--BOUND\r\n" +
		"Content-Type: application/pdf; name=\"po.pdf\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"Content-Disposition: attachment; filename=\"po.pdf\"\r\n\r\n" +
		"JVBERi0xLjQKJWZha2U=\r\n" +
		"--BOUND--\r\n"

	msg, err := mail.ReadMessage(bytes.NewBufferString(raw))
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	_, atts, err := walkMIME(msg)
	if err != nil {
		t.Fatalf("walkMIME: %v", err)
	}
	if len(atts) != 1 {
		t.Fatalf("attachments = %d, want 1", len(atts))
	}
	if got := string(atts[0].Bytes); got != "%PDF-1.4\n%fake" {
		t.Errorf("attachment bytes = %q, want decoded PDF content", got)
	}
	if !bytes.HasPrefix(atts[0].Bytes, []byte("%PDF-")) {
		t.Error("attachment does not start with the PDF magic — still encoded")
	}
}

// quoted-printable bodies otherwise keep their =3D / =20 / soft-break
// artifacts, which then get filed as the email's Markdown document.
func TestWalkMIME_DecodesQuotedPrintableBody(t *testing.T) {
	raw := "From: a@b.c\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=B\r\n\r\n" +
		"--B\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n\r\n" +
		"Total is =E2=82=AC50 =E2=80=94 pay by Friday=\r\n please.\r\n" +
		"--B--\r\n"

	msg, err := mail.ReadMessage(bytes.NewBufferString(raw))
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	body, _, err := walkMIME(msg)
	if err != nil {
		t.Fatalf("walkMIME: %v", err)
	}
	if !strings.Contains(body, "€50") {
		t.Errorf("body = %q, want decoded UTF-8 (€50)", body)
	}
	if strings.Contains(body, "=E2") || strings.Contains(body, "=\r\n") {
		t.Errorf("body still carries quoted-printable artifacts: %q", body)
	}
}

// An unencoded (7bit) part must pass through untouched.
func TestWalkMIME_PlainPartUnchanged(t *testing.T) {
	raw := "From: a@b.c\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=B\r\n\r\n" +
		"--B\r\n" +
		"Content-Type: text/plain\r\n\r\n" +
		"hello world\r\n" +
		"--B--\r\n"
	msg, _ := mail.ReadMessage(bytes.NewBufferString(raw))
	body, _, err := walkMIME(msg)
	if err != nil {
		t.Fatalf("walkMIME: %v", err)
	}
	if !strings.Contains(body, "hello world") {
		t.Errorf("body = %q, want %q", body, "hello world")
	}
}

// Subjects arrive as RFC 2047 encoded-words; undecoded they became the
// document TITLE, e.g. "=?UTF-8?q?[Docker]_You_+_Docker?=.md".
func TestDecodeHeader(t *testing.T) {
	cases := []struct{ in, want string }{
		{"=?UTF-8?q?[Docker]_You_+_Docker?=", "[Docker] You + Docker"},
		{"=?utf-8?B?4oKsNTAgaW52b2ljZQ==?=", "€50 invoice"},
		{"Plain subject", "Plain subject"},
		{"", ""},
	}
	for _, c := range cases {
		if got := decodeHeader(c.in); got != c.want {
			t.Errorf("decodeHeader(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
