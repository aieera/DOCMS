package service

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/aieera/sedoc/services/signature/internal/signer"
)

// fakeSigner records each Sign call and appends a per-call marker to the PDF so
// the test can assert the revisions chain (each Sign's input == the prior Sign's
// output) and are applied in order — the DoD's "both signers cryptographically
// sign, in order" without the live DSS sidecar.
type fakeSigner struct {
	calls   []signer.Request
	inputs  [][]byte
	counter int
}

func (f *fakeSigner) Sign(_ context.Context, req signer.Request) (*signer.Response, error) {
	f.calls = append(f.calls, req)
	f.inputs = append(f.inputs, append([]byte(nil), req.PDFBytes...))
	f.counter++
	out := append(append([]byte(nil), req.PDFBytes...), []byte(fmt.Sprintf("\n%%rev-%s", req.FieldName))...)
	return &signer.Response{PDFBytes: out, Level: signer.LevelBLT, Fingerprint: fmt.Sprintf("fp%d", f.counter)}, nil
}

func (f *fakeSigner) Verify(_ context.Context, _ []byte) (*signer.VerificationReport, error) {
	return &signer.VerificationReport{SignatureCount: f.counter, LTVEnabled: true}, nil
}

func TestApplyCeremonyRevisions_PerSignerChainAndOrder(t *testing.T) {
	fs := &fakeSigner{}
	original := []byte("%PDF-1.7 original")
	signers := []CeremonySigner{
		{Name: "Alice", Email: "alice@acme.com", FieldName: "Signature_a"},
		{Name: "Bob", Email: "bob@acme.com", FieldName: "Signature_b"},
	}
	out, level, fp, err := applyCeremonyRevisions(context.Background(), fs, "t1", "http://tsa.test", original, signers)
	if err != nil {
		t.Fatal(err)
	}

	// N signers + 1 org seal = N+1 Sign calls.
	if len(fs.calls) != 3 {
		t.Fatalf("want 3 Sign calls (2 signers + org seal), got %d", len(fs.calls))
	}
	// Order + attribution.
	if fs.calls[0].SignerName != "Alice" || fs.calls[1].SignerName != "Bob" || fs.calls[2].SignerName != "SeDoc Organizational Seal" {
		t.Fatalf("wrong signer order: %q, %q, %q", fs.calls[0].SignerName, fs.calls[1].SignerName, fs.calls[2].SignerName)
	}
	// TSA + level threaded on every revision (so B-LT/LTV is requested).
	for i, c := range fs.calls {
		if c.TSAURL != "http://tsa.test" || c.Level != signer.LevelBLT || c.Mode != signer.ModeServerHSM {
			t.Fatalf("call %d missing TSA/level/mode: %+v", i, c)
		}
	}
	// Chaining: each Sign's input equals the previous Sign's output.
	if !bytes.Equal(fs.inputs[0], original) {
		t.Fatal("first revision must sign the original PDF")
	}
	for i := 1; i < len(fs.inputs); i++ {
		prevOut := append(append([]byte(nil), fs.inputs[i-1]...), []byte(fmt.Sprintf("\n%%rev-%s", fs.calls[i-1].FieldName))...)
		if !bytes.Equal(fs.inputs[i], prevOut) {
			t.Fatalf("revision %d did not chain onto revision %d's output", i, i-1)
		}
	}
	// Final output carries every revision marker.
	for _, want := range []string{"%rev-Signature_a", "%rev-Signature_b", "%rev-OrgSeal"} {
		if !bytes.Contains(out, []byte(want)) {
			t.Fatalf("final PDF missing revision %q", want)
		}
	}
	if level != string(signer.LevelBLT) || fp != "fp3" {
		t.Fatalf("unexpected level/fingerprint: %q %q", level, fp)
	}
}

func TestApplyCeremonyRevisions_EmptySignersStillSeals(t *testing.T) {
	fs := &fakeSigner{}
	out, _, _, err := applyCeremonyRevisions(context.Background(), fs, "t1", "", []byte("%PDF"), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Just the org seal.
	if len(fs.calls) != 1 || fs.calls[0].FieldName != "OrgSeal" {
		t.Fatalf("empty signers should still apply one org seal, got %d calls", len(fs.calls))
	}
	if !bytes.Contains(out, []byte("%rev-OrgSeal")) {
		t.Fatal("org seal not applied")
	}
}
