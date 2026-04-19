package scanner

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// Blocklist tests — these are the first line of defence against uploads
// of executables. A silent regression (e.g. someone "cleaning up" the
// blocklist) would let .exe/.bat through.

func TestIsBlockedMIME(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"application/x-msdownload", true},
		{"APPLICATION/X-MSDOWNLOAD", true}, // case insensitive
		{"  application/x-ms-installer  ", true},
		{"application/x-executable", true},
		{"application/pdf", false},
		{"image/png", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsBlockedMIME(tc.in); got != tc.want {
			t.Errorf("IsBlockedMIME(%q): got %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestIsBlockedExtension(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"malware.exe", true},
		{"MALWARE.EXE", true}, // case insensitive
		{"autorun.bat", true},
		{"installer.msi", true},
		{"document.pdf", false},
		{"photo.png", false},
		{"", false},
		{"noextension", false},
		{".exe", true}, // bare extension
	}
	for _, tc := range cases {
		if got := IsBlockedExtension(tc.in); got != tc.want {
			t.Errorf("IsBlockedExtension(%q): got %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestMIMEMatchesDeclared(t *testing.T) {
	detectedPNG := DetectedType{MIME: "image/png", IsKnown: true}
	detectedJPEG := DetectedType{MIME: "image/jpeg", IsKnown: true}
	unknown := DetectedType{IsKnown: false}

	cases := []struct {
		name     string
		declared string
		detected DetectedType
		want     bool
	}{
		{"exact match", "image/png", detectedPNG, true},
		{"case insensitive", "Image/PNG", detectedPNG, true},
		{"jpg alias matches jpeg", "image/jpg", detectedJPEG, true},
		{"pjpeg alias matches jpeg", "image/pjpeg", detectedJPEG, true},
		{"x-png alias matches png", "image/x-png", detectedPNG, true},
		{"empty declared: always matches", "", detectedPNG, true},
		{"unknown detection: always matches", "image/png", unknown, true},
		{"outright mismatch: rejected", "application/pdf", detectedPNG, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MIMEMatchesDeclared(tc.declared, tc.detected); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPeekHead_YieldsCompleteStream(t *testing.T) {
	// Reader smaller than 262 bytes — PeekHead must still return it whole
	// via io.MultiReader. Regression catch: if PeekHead ever uses io.Read
	// instead of io.ReadFull, short reads leave bytes behind.
	payload := bytes.Repeat([]byte("a"), 10)
	head, r, err := PeekHead(bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("PeekHead: %v", err)
	}
	if len(head) != 10 {
		t.Errorf("short head: %d bytes", len(head))
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("round-trip: got %q, want %q", got, payload)
	}
}

func TestPeekHead_OverSizeStream(t *testing.T) {
	// Larger than 262 bytes — head is exactly 262, rest is the remaining
	// bytes available on the returned reader.
	payload := bytes.Repeat([]byte("x"), 500)
	head, r, err := PeekHead(bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("PeekHead: %v", err)
	}
	if len(head) != 262 {
		t.Errorf("head size: %d, want 262", len(head))
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read rest: %v", err)
	}
	if len(got) != 500 {
		t.Errorf("total bytes: %d, want 500", len(got))
	}
}

func TestDetectFromBytes_EmptyReturnsZero(t *testing.T) {
	d, err := DetectFromBytes(nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if d.IsKnown || d.MIME != "" || d.Extension != "" {
		t.Errorf("empty input should return empty DetectedType, got %+v", d)
	}
}

func TestDetectFromBytes_PNG(t *testing.T) {
	// PNG magic bytes: 89 50 4E 47 0D 0A 1A 0A
	png := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52}
	d, err := DetectFromBytes(png)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !d.IsKnown {
		t.Fatal("PNG header should be known")
	}
	if !strings.EqualFold(d.MIME, "image/png") {
		t.Errorf("MIME: got %q, want image/png", d.MIME)
	}
	if d.Extension != ".png" {
		t.Errorf("extension: got %q, want .png", d.Extension)
	}
}
