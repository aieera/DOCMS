package sso

// Wave 6 Prompt 6.3 — unpredictable X.509 serial minting.
//
// RFC 5280 §4.1.2.2 mandates that serials be unpredictable. The
// historical bug pattern is `math/rand` seeded with a predictable
// value (often zero, i.e. the same serial on every boot). We use
// crypto/rand and the full 128-bit range; the CI guard at
// scripts/check-no-math-rand.sh fails the build if math/rand v1 is
// ever imported in this package.

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

// generateSerial returns a uniformly random big.Int in [1, 2^128).
// Zero is skipped on the vanishingly rare chance it is drawn so the
// value is always a valid, non-zero X.509 serial.
func generateSerial() (*big.Int, error) {
	max := new(big.Int).Lsh(big.NewInt(1), 128)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return nil, fmt.Errorf("crypto/rand: %w", err)
	}
	if n.Sign() == 0 {
		// Re-draw once; astronomically unlikely to happen.
		n, err = rand.Int(rand.Reader, max)
		if err != nil {
			return nil, fmt.Errorf("crypto/rand retry: %w", err)
		}
		if n.Sign() == 0 {
			return nil, fmt.Errorf("generated zero serial twice — RNG broken")
		}
	}
	return n, nil
}
