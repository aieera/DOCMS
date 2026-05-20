package license

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// rawClaims maps the JWT body. We translate to the public Claims
// struct after Parse to keep the wire format separate from the
// internal type. JWT timestamps are seconds-since-epoch on the wire.
type rawClaims struct {
	jwt.RegisteredClaims
	TenantName   string       `json:"tenant_name"`
	SeatLimit    int          `json:"seat_limit"`
	FeatureFlags FeatureFlags `json:"feature_flags"`
	IssuedTo     string       `json:"issued_to"`
	IssuedBy     string       `json:"issued_by"`
	GraceDays    int          `json:"grace_days"`
}

// ParsePublicKeyPEM converts the bundled PEM block to an rsa.PublicKey.
// Fail-closed: bad PEM means we can't verify anything.
func ParsePublicKeyPEM(pemStr string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("license: pem decode failed")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("license: parse public key: %w", err)
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("license: bundled key is not RSA")
	}
	return rsaPub, nil
}

// Verify parses + verifies a JWT against the supplied public key and
// returns the parsed Claims. Returns an error on signature failure,
// alg confusion, missing required claims, or `exp` being more than
// GraceDays in the past (so callers can fail-closed).
//
// Caller decides how to react to a *Claims with DerivedStatus ==
// StatusGrace (typically: allow reads, return 423 on writes).
func Verify(tokenString string, pubKey *rsa.PublicKey) (*Claims, error) {
	var raw rawClaims
	tok, err := jwt.ParseWithClaims(tokenString, &raw, func(t *jwt.Token) (interface{}, error) {
		// Reject alg confusion attacks — only accept the same alg
		// the signer used. RS256 is the only algorithm we ship.
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("license: unexpected signing method %v", t.Header["alg"])
		}
		return pubKey, nil
	},
		// Don't auto-reject expired tokens; we handle grace ourselves
		// in DerivedStatus. Without this option ParseWithClaims would
		// return an error for any expired JWT and we'd never reach
		// the grace branch.
		jwt.WithoutClaimsValidation(),
	)
	if err != nil {
		return nil, fmt.Errorf("license: parse: %w", err)
	}
	if !tok.Valid {
		return nil, errors.New("license: signature invalid")
	}

	// Required claims
	if raw.Subject == "" {
		return nil, errors.New("license: missing sub (tenant id)")
	}
	if raw.TenantName == "" {
		return nil, errors.New("license: missing tenant_name")
	}
	if raw.ExpiresAt == nil {
		return nil, errors.New("license: missing exp")
	}

	// Reject licenses past `exp + grace_days` immediately. Service
	// will treat this as fatal at startup unless dev mode.
	graceDays := raw.GraceDays
	if graceDays <= 0 {
		graceDays = 30 // default from ADR 0095
	}
	graceEnd := raw.ExpiresAt.Time.Add(time.Duration(graceDays) * 24 * time.Hour)
	if time.Now().After(graceEnd) {
		return nil, fmt.Errorf("license: expired more than %d days ago", graceDays)
	}

	out := &Claims{
		Issuer:       raw.Issuer,
		Subject:      raw.Subject,
		TenantName:   raw.TenantName,
		SeatLimit:    raw.SeatLimit,
		FeatureFlags: raw.FeatureFlags,
		IssuedTo:     raw.IssuedTo,
		IssuedBy:     raw.IssuedBy,
		GraceDays:    graceDays,
	}
	if raw.IssuedAt != nil {
		out.IssuedAt = raw.IssuedAt.Time
	}
	out.ExpiresAt = raw.ExpiresAt.Time
	return out, nil
}
