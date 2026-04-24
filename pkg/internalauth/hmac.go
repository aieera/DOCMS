package internalauth

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// InternalSignatureHeader carries the HMAC signature.
//
// Format: "t=<unix_seconds>;s=<hex_hmac_sha256>"
// The signed payload is method + "\n" + path + "\n" + timestamp +
// "\n" + body. Binding the method + path prevents a captured
// signature from being replayed against a different endpoint.
const InternalSignatureHeader = "X-Internal-Signature"

// SignRequest produces the header value a caller would send. Helper
// for cross-service HTTP clients and tests; not used by server middleware.
func SignRequest(secret string, method, path string, body []byte, timestamp int64) string {
	payload := buildPayload(method, path, timestamp, body)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return fmt.Sprintf("t=%d;s=%s", timestamp, hex.EncodeToString(mac.Sum(nil)))
}

func (v *Verifier) verifyHMAC(r *http.Request) error {
	raw := r.Header.Get(InternalSignatureHeader)
	if raw == "" {
		record("none", "missing")
		return errors.New("missing " + InternalSignatureHeader)
	}
	ts, sig, ok := parseSignature(raw)
	if !ok {
		record("hmac", "bad_sig")
		return errors.New("malformed " + InternalSignatureHeader)
	}

	now := v.now().Unix()
	delta := now - ts
	if delta < 0 {
		delta = -delta
	}
	if delta > v.cfg.ClockSkewSecs {
		record("hmac", "skew")
		return fmt.Errorf("timestamp skew %ds exceeds window %ds", delta, v.cfg.ClockSkewSecs)
	}

	// Read body (up to a sane cap) and restore it for the next
	// handler. The internal sweep endpoints take tiny JSON so a
	// 1 MiB cap is generous.
	const maxInternalBody = 1 << 20
	body, err := io.ReadAll(io.LimitReader(r.Body, maxInternalBody+1))
	if err != nil {
		record("hmac", "bad_sig")
		return fmt.Errorf("read body: %w", err)
	}
	if len(body) > maxInternalBody {
		record("hmac", "bad_sig")
		return errors.New("internal request body exceeds 1 MiB")
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	want := hmacSign([]byte(v.cfg.HMACSecret), buildPayload(r.Method, r.URL.Path, ts, body))
	if !hmac.Equal([]byte(sig), []byte(want)) {
		record("hmac", "bad_sig")
		return errors.New("invalid signature")
	}
	return nil
}

func hmacSign(secret, payload []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func buildPayload(method, path string, timestamp int64, body []byte) []byte {
	// Stable canonicalisation: upper-case method, raw path, decimal
	// seconds, LF separators, then body verbatim.
	buf := bytes.Buffer{}
	buf.Grow(len(method) + len(path) + 24 + len(body))
	buf.WriteString(strings.ToUpper(method))
	buf.WriteByte('\n')
	buf.WriteString(path)
	buf.WriteByte('\n')
	buf.WriteString(strconv.FormatInt(timestamp, 10))
	buf.WriteByte('\n')
	buf.Write(body)
	return buf.Bytes()
}

func parseSignature(raw string) (ts int64, sig string, ok bool) {
	parts := strings.Split(raw, ";")
	if len(parts) != 2 {
		return 0, "", false
	}
	var seenT, seenS bool
	for _, p := range parts {
		p = strings.TrimSpace(p)
		switch {
		case strings.HasPrefix(p, "t="):
			v, err := strconv.ParseInt(p[2:], 10, 64)
			if err != nil {
				return 0, "", false
			}
			ts = v
			seenT = true
		case strings.HasPrefix(p, "s="):
			sig = p[2:]
			seenS = true
		default:
			return 0, "", false
		}
	}
	if !seenT || !seenS || sig == "" {
		return 0, "", false
	}
	return ts, sig, true
}
