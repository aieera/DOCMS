package service

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/services/signature/internal/pades"
	"github.com/aieera/sedoc/services/signature/internal/signer"
)

// ltvDSSSigner is a stand-in for the JVM DSS sidecar in pure-Go tests: each
// Sign appends one incremental "revision" — a parseable /Sig dict (so the
// pades Verifier counts it) plus a /DSS dict carrying LTV material (so the
// Verifier reports LTVEnabled) — and reports PAdES-B-LT. It is NOT
// cryptographically valid (the real sidecar produces genuine CMS/OCSP/TSA
// material; that path is covered by dss_sidecar_test + the integration lane).
// What it lets us prove without a JVM: the REAL ceremony chaining produces an
// artifact the REAL pades LTV validator recognises as LTV-enabled with every
// per-signer revision present.
type ltvDSSSigner struct{ n int }

func (s *ltvDSSSigner) Sign(_ context.Context, req signer.Request) (*signer.Response, error) {
	s.n++
	// Non-zero, per-revision-unique hex Contents (matches the realistic shape
	// the parser expects; short/zeroed contents get trimmed away).
	contents := strings.Repeat(fmt.Sprintf("%02x", s.n%256), 120)
	rev := fmt.Sprintf(
		"\n%d 0 obj <</Type /Sig /SubFilter /adbe.pkcs7.detached "+
			"/Name (%s) /Reason (%s) /ByteRange [0 80 200 100] /Contents <%s>>> endobj\n"+
			"%d 0 obj <</Type /DSS /OCSPs [%d 0 R]>> endobj\n",
		100+s.n, req.SignerName, req.Reason, contents, 200+s.n, 300+s.n)
	out := append(append([]byte{}, req.PDFBytes...), []byte(rev)...)
	return &signer.Response{PDFBytes: out, Level: signer.LevelBLT, Fingerprint: fmt.Sprintf("fp%d", s.n)}, nil
}

func (s *ltvDSSSigner) Verify(context.Context, []byte) (*signer.VerificationReport, error) {
	return &signer.VerificationReport{}, nil
}

// The DoD proof (pure-Go half): a 2-signer ceremony produces PAdES-B-LT output
// whose LTV material the pades validator confirms, with all N+1 revisions
// present. The Temporal orchestration half lives in the workflow testsuite
// (services/workflow SignatureWorkflow test).
func TestSealCeremony_TwoSigners_ProducesLTVValidatedByVerifier(t *testing.T) {
	fs := &ltvDSSSigner{}
	original := []byte("%PDF-1.7\n1 0 obj <</Type /Catalog /Pages 2 0 R>> endobj\n%%EOF\n")
	signers := []CeremonySigner{
		{Name: "Alice Signer", Email: "alice@example.com", FieldName: "Signature_1"},
		{Name: "Bob Signer", Email: "bob@example.com", FieldName: "Signature_2"},
	}

	sealed, level, fp, err := applyCeremonyRevisions(context.Background(), fs, "tenant-1", "http://tsa.test", original, signers)
	require.NoError(t, err)
	require.Equal(t, string(signer.LevelBLT), level, "a 2-signer ceremony (with TSA) must achieve PAdES-B-LT (LTV)")
	require.NotEmpty(t, fp)

	// The REAL pades LTV validator, over the REAL ceremony output.
	rep, verr := pades.NewVerifier(pades.VerifierOptions{}).Validate(context.Background(), sealed)
	require.NoError(t, verr, "the sealed ceremony PDF must validate (parse + report)")
	require.Equal(t, 3, rep.SignatureCount, "2 signers + 1 organizational seal = 3 PAdES revisions")
	require.True(t, rep.LTVEnabled, "the pades validator must confirm the ceremony output carries LTV material (/DSS)")
}
