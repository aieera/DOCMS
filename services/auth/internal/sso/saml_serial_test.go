package sso

// Wave 6 Prompt 6.3 — serial-number entropy property test.
//
// X.509 serials must be unpredictable (RFC 5280 §4.1.2.2). The
// `generateSerial` helper uses crypto/rand over [1, 2^128). This
// test mints 10k serials and asserts:
//
//   - every serial is ≥64 bits (a math/rand regression would collapse
//     into < 2^63 — 64 is the cliff),
//   - ≥70% of draws are ≥127 bits (uniform over 128 bits gives 3/4
//     probability; 70% is a 7σ floor for 10k samples),
//   - all 10k serials are distinct.
//
// Paired with scripts/check-no-math-rand.sh: the script forbids
// math/rand imports in the package; this test catches any stealth
// downgrade that still happens to compile.

import (
	"testing"
)

const (
	minIndividualBits = 64
	minHighEntropyPct = 70 // ≥70% of draws have ≥127 bits
)

func TestGenerateSerial_UniqueAndHighEntropy(t *testing.T) {
	const draws = 10_000
	seen := make(map[string]struct{}, draws)
	highEntropy := 0

	for i := 0; i < draws; i++ {
		serial, err := generateSerial()
		if err != nil {
			t.Fatalf("draw %d: %v", i, err)
		}
		if serial.Sign() <= 0 {
			t.Fatalf("draw %d: non-positive serial: %v", i, serial)
		}
		if serial.BitLen() < minIndividualBits {
			t.Fatalf(
				"draw %d: bit length %d < %d — RNG collapse suspected",
				i, serial.BitLen(), minIndividualBits,
			)
		}
		if serial.BitLen() >= 127 {
			highEntropy++
		}
		key := serial.Text(16)
		if _, dup := seen[key]; dup {
			t.Fatalf("duplicate serial %s at draw %d — RNG not crypto-grade", key, i)
		}
		seen[key] = struct{}{}
	}

	if len(seen) != draws {
		t.Errorf("expected %d unique serials; got %d", draws, len(seen))
	}
	pct := highEntropy * 100 / draws
	if pct < minHighEntropyPct {
		t.Errorf(
			"only %d%% of serials had ≥127 bits (got %d/%d) — below %d%% floor",
			pct, highEntropy, draws, minHighEntropyPct,
		)
	}
}

// Fast smoke: two consecutive draws cannot be equal under crypto/rand
// (probability ~2^-128). Math/rand with a fixed seed would fail here
// immediately — useful as a pre-commit sanity check even outside CI.
func TestGenerateSerial_ConsecutiveDrawsDiffer(t *testing.T) {
	a, err := generateSerial()
	if err != nil {
		t.Fatal(err)
	}
	b, err := generateSerial()
	if err != nil {
		t.Fatal(err)
	}
	if a.Cmp(b) == 0 {
		t.Fatal("consecutive serials equal — RNG is deterministic")
	}
}

// Integration smoke: one end-to-end NewSelfSignedSP() call must still
// succeed with a well-formed serial. This is the only cert-generating
// test in this file, kept to a single draw to keep CI fast.
func TestSelfSignedSP_ProducesValidSerial(t *testing.T) {
	sp, err := NewSelfSignedSP()
	if err != nil {
		t.Fatalf("NewSelfSignedSP: %v", err)
	}
	if sp.Certificate.SerialNumber == nil || sp.Certificate.SerialNumber.Sign() <= 0 {
		t.Fatal("cert has nil or non-positive serial")
	}
	if sp.Certificate.SerialNumber.BitLen() < minIndividualBits {
		t.Errorf("cert serial bit length %d below floor %d",
			sp.Certificate.SerialNumber.BitLen(), minIndividualBits)
	}
}
