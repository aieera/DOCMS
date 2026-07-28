package signer

// Wave 9 Prompt 9.2 — signer interface + MockSigner tests.
//
// Real PAdES validation (pdf-signature-validator CLI against Adobe
// Reader output) is the Wave 9.2b DoD once the DSS sidecar ships.
// Here we pin the Signer-interface contract:
//
//   - validation errors return ErrInvalidRequest
//   - MockSigner.Sign is deterministic in shape (append marker)
//   - MockSigner.Verify round-trips every marker back into SignatureInfo
//   - Factory rejects unknown SEDOC_SIGNER values
//   - DSSSidecarSigner returns ErrNotConfigured per package doc

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

const samplePDF = "%PDF-1.7\n1 0 obj<<>>endobj\nxref\n0 1\n0000000000 65535 f\ntrailer<<>>\n%%EOF\n"

func TestMockSigner_HappyPath(t *testing.T) {
	s := NewMockSigner()
	out, err := s.Sign(context.Background(), Request{
		PDFBytes:    []byte(samplePDF),
		SignerName:  "Alice",
		SignerEmail: "alice@example.com",
		Level:       LevelBLT,
		Mode:        ModeServerHSM,
		KMSAlias:    "vaultdms/tenant/xyz",
		FieldName:   "Signer_1",
		Reason:      "unit test",
	})
	require.NoError(t, err)
	require.NotEmpty(t, out.PDFBytes)
	require.Equal(t, LevelBLT, out.Level)
	require.NotEmpty(t, out.Fingerprint)
	require.Contains(t, string(out.PDFBytes), "%Signer: Alice")
}

func TestMockSigner_RejectsEmptyPDF(t *testing.T) {
	s := NewMockSigner()
	_, err := s.Sign(context.Background(), Request{
		SignerName: "Alice", Mode: ModeServerHSM, KMSAlias: "a",
	})
	require.ErrorIs(t, err, ErrInvalidRequest)
}

func TestMockSigner_RejectsServerHSMWithoutKMSAlias(t *testing.T) {
	s := NewMockSigner()
	_, err := s.Sign(context.Background(), Request{
		PDFBytes: []byte(samplePDF), SignerName: "Alice", Mode: ModeServerHSM,
	})
	require.ErrorIs(t, err, ErrInvalidRequest)
}

func TestMockSigner_RejectsUserHeldWithoutCertChain(t *testing.T) {
	s := NewMockSigner()
	_, err := s.Sign(context.Background(), Request{
		PDFBytes: []byte(samplePDF), SignerName: "Alice", Mode: ModeUserHeld,
		DetachedSignature: []byte("sig"),
	})
	require.ErrorIs(t, err, ErrInvalidRequest)
}

func TestMockSigner_VerifyRoundTripsMultipleSigners(t *testing.T) {
	s := NewMockSigner()
	out1, err := s.Sign(context.Background(), Request{
		PDFBytes: []byte(samplePDF), SignerName: "Alice", SignerEmail: "a@x",
		Level: LevelBLT, Mode: ModeServerHSM, KMSAlias: "k", FieldName: "Signer_1",
	})
	require.NoError(t, err)

	out2, err := s.Sign(context.Background(), Request{
		PDFBytes: out1.PDFBytes, SignerName: "Bob", SignerEmail: "b@x",
		Level: LevelBLT, Mode: ModeServerHSM, KMSAlias: "k", FieldName: "Signer_2",
	})
	require.NoError(t, err)

	report, err := s.Verify(context.Background(), out2.PDFBytes)
	require.NoError(t, err)
	require.Equal(t, 2, report.SignatureCount)
	require.Equal(t, "Alice", report.Signatures[0].SignerName)
	require.Equal(t, "Bob", report.Signatures[1].SignerName)
	// MockSigner NEVER claims LTV — spelled out in the package doc.
	require.False(t, report.LTVEnabled)
}

func TestFactory_RejectsUnknown(t *testing.T) {
	t.Setenv("SEDOC_SIGNER", "whoknows")
	_, err := FromEnv("")
	require.Error(t, err)
}

// TestFactory_RequiresExplicitSigner pins the fail-closed contract (Epic 5 #11):
// an UNSET SEDOC_SIGNER must be an error, not a silent MockSigner — defaulting to
// the non-PAdES mock would ship Adobe-invalid signatures if a production deploy
// forgot to set it. Dev/CI must set SEDOC_SIGNER=mock explicitly.
func TestFactory_RequiresExplicitSigner(t *testing.T) {
	_ = os.Unsetenv("SEDOC_SIGNER")
	_, err := FromEnv("")
	require.Error(t, err)
}

func TestFactory_MockWhenSet(t *testing.T) {
	t.Setenv("SEDOC_SIGNER", "mock")
	s, err := FromEnv("")
	require.NoError(t, err)
	_, ok := s.(*MockSigner)
	require.True(t, ok)
}

// TestDSSSidecarSigner_ValidatesInput pins that the live DSS client
// short-circuits invalid requests in validateCommon BEFORE any network call,
// so callers get a fast, typed ErrInvalidRequest rather than a dial timeout.
// (The shell's old ErrNotConfigured behavior is gone — Wave 9.2b made Sign
// real; the live round-trip is covered by TestDSSSidecarSignVerifyLive.)
func TestDSSSidecarSigner_ValidatesInput(t *testing.T) {
	s := NewDSSSidecarSigner("localhost:6060")
	// Empty PDF → ErrInvalidRequest before dialing.
	_, err := s.Sign(context.Background(), Request{
		SignerName: "Alice", Mode: ModeServerHSM, KMSAlias: "k",
	})
	require.True(t, errors.Is(err, ErrInvalidRequest))
	// ServerHSM without KMSAlias → ErrInvalidRequest before dialing.
	_, err = s.Sign(context.Background(), Request{
		PDFBytes: []byte(samplePDF), SignerName: "Alice", Mode: ModeServerHSM,
	})
	require.True(t, errors.Is(err, ErrInvalidRequest))
}
