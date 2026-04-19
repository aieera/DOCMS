package scanner

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"io"
)

// SHA256TeeReader wraps r so every byte read also feeds a running SHA-256.
// Call Sum() after the reader is exhausted to get the hex digest.
type SHA256TeeReader struct {
	r io.Reader
	h interface {
		io.Writer
		Sum([]byte) []byte
	}
}

// NewSHA256TeeReader constructs a tee-hash reader.
func NewSHA256TeeReader(r io.Reader) *SHA256TeeReader {
	return &SHA256TeeReader{r: r, h: sha256.New()}
}

// Read delegates to the underlying reader and updates the running hash.
func (t *SHA256TeeReader) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if n > 0 {
		_, _ = t.h.Write(p[:n])
	}
	return n, err
}

// Sum returns the hex-encoded SHA-256 digest of the bytes read so far.
// Call only after Read has returned io.EOF.
func (t *SHA256TeeReader) Sum() string {
	return hex.EncodeToString(t.h.Sum(nil))
}

// VerifyHash returns true when actual matches expected using constant-time
// comparison. Both are hex strings; length mismatch returns false without
// comparison.
func VerifyHash(expected, actual string) bool {
	if len(expected) != len(actual) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) == 1
}
