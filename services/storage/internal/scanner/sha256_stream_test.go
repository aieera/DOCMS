package scanner

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"
)

// The tee reader is on the hot upload path — every byte of every uploaded
// file passes through it. A bug here either corrupts the stored hash or
// silently short-reads the upload.

func TestSHA256TeeReader_SingleRead(t *testing.T) {
	payload := []byte("hello world")
	tee := NewSHA256TeeReader(bytes.NewReader(payload))

	got, err := io.ReadAll(tee)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("tee corrupted stream: got %q, want %q", got, payload)
	}

	want := sha256Hex(payload)
	if tee.Sum() != want {
		t.Errorf("Sum: got %s, want %s", tee.Sum(), want)
	}
}

func TestSHA256TeeReader_ChunkedReads(t *testing.T) {
	// Simulate an upload read in odd-sized chunks. Bug hunt: if Read
	// only hashes one chunk (e.g. due to re-assignment of p), the final
	// Sum() won't match.
	payload := bytes.Repeat([]byte("data"), 500) // 2000 bytes
	tee := NewSHA256TeeReader(bytes.NewReader(payload))
	buf := make([]byte, 37) // deliberately awkward size
	var got []byte
	for {
		n, err := tee.Read(buf)
		if n > 0 {
			got = append(got, buf[:n]...)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read: %v", err)
		}
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("chunked tee corrupted stream")
	}
	if tee.Sum() != sha256Hex(payload) {
		t.Error("chunked Sum() mismatch")
	}
}

func TestSHA256TeeReader_EmptyInput(t *testing.T) {
	tee := NewSHA256TeeReader(bytes.NewReader(nil))
	_, err := io.ReadAll(tee)
	if err != nil {
		t.Fatalf("ReadAll empty: %v", err)
	}
	// SHA-256 of empty: e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
	if tee.Sum() != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Errorf("empty digest wrong: %s", tee.Sum())
	}
}

func TestVerifyHash_Match(t *testing.T) {
	if !VerifyHash("abc", "abc") {
		t.Error("equal strings should match")
	}
}

func TestVerifyHash_LengthMismatch(t *testing.T) {
	if VerifyHash("abc", "abcd") {
		t.Error("different-length hashes should not match")
	}
}

func TestVerifyHash_DifferentContent(t *testing.T) {
	if VerifyHash("abc", "xyz") {
		t.Error("different hashes should not match")
	}
}

func TestVerifyHash_CaseSensitive(t *testing.T) {
	// SHA-256 hex is normally lowercase. If the server ever emits uppercase
	// by accident, the equality fails — which is what we want (force
	// everyone onto a single canonical form).
	if VerifyHash("abc", "ABC") {
		t.Error("hex hashes are case-sensitive")
	}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return strings.ToLower(hex.EncodeToString(sum[:]))
}
