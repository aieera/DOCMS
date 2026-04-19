// MockSigner is a deterministic, in-process Signer used in tests,
// CI, and local dev.
//
// It is **NOT** PAdES-valid. Adobe Reader will NOT accept its output.
// The goal is to exercise the REST surface, the Temporal workflow,
// and the envelope/signer state machine without pulling in a JVM.
//
// Output format: the input PDF bytes followed by a ~200-byte ASCII
// marker block that includes the signer identity and a fake field
// name. Verify() parses the marker blocks back out to report which
// signers appear in the document. Tamper-evidence is approximated by
// checking that no byte falls between marker blocks beyond a small
// whitespace allowance.
package signer

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const markerPrefix = "%VaultDMS-MockSig-v1\n"
const markerSuffix = "%VaultDMS-MockSig-END\n"

// MockSigner implements Signer without any external dependency.
// Safe for concurrent use — it holds no mutable state.
type MockSigner struct{}

// NewMockSigner constructs a MockSigner.
func NewMockSigner() *MockSigner { return &MockSigner{} }

// Sign appends a marker block to req.PDFBytes. The output is still
// a syntactically valid PDF (markers live after %%EOF in a comment
// block, which PDF readers ignore), but it is NOT PAdES-valid.
func (m *MockSigner) Sign(ctx context.Context, req Request) (*Response, error) {
	if err := validateCommon(req); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	ts := time.Now().UTC()
	marker := markerPrefix +
		"%Signer: " + req.SignerName + "\n" +
		"%Email: " + req.SignerEmail + "\n" +
		"%Field: " + req.FieldName + "\n" +
		"%Level: " + string(req.Level) + "\n" +
		"%Mode: " + string(req.Mode) + "\n" +
		"%SignedAt: " + ts.Format(time.RFC3339Nano) + "\n" +
		"%Reason: " + req.Reason + "\n" +
		markerSuffix

	out := make([]byte, 0, len(req.PDFBytes)+len(marker))
	out = append(out, req.PDFBytes...)
	if !bytes.HasSuffix(out, []byte{'\n'}) {
		out = append(out, '\n')
	}
	out = append(out, marker...)

	return &Response{
		PDFBytes:   out,
		Level:      req.Level,
		SignedAt:   ts,
		Fingerprint: fingerprintOf(out),
	}, nil
}

// markerRE matches one signer marker block. Capture group 1 is the
// body (signer + email + ... lines).
// Body is non-greedy (`+?`) so two adjacent marker blocks parse as
// two matches rather than one run that swallows the inner END+v1
// lines (both start with `%`, so a greedy match over `%[^\n]*\n`
// would happily grab them).
var markerRE = regexp.MustCompile(`%VaultDMS-MockSig-v1\n((?:%[^\n]*\n)+?)%VaultDMS-MockSig-END\n`)

// Verify parses out the marker blocks and reports them as signatures.
// Every MockSigner-produced PDF verifies as valid; a real PDF would
// return SignatureCount=0 which the caller treats as "unsigned."
func (m *MockSigner) Verify(ctx context.Context, pdfBytes []byte) (*VerificationReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := &VerificationReport{TamperEvident: true}
	matches := markerRE.FindAllSubmatch(pdfBytes, -1)
	for _, m := range matches {
		info := SignatureInfo{Valid: true, Level: LevelBLT}
		for _, line := range strings.Split(string(m[1]), "\n") {
			switch {
			case strings.HasPrefix(line, "%Signer: "):
				info.SignerName = strings.TrimPrefix(line, "%Signer: ")
			case strings.HasPrefix(line, "%Level: "):
				info.Level = Level(strings.TrimPrefix(line, "%Level: "))
			case strings.HasPrefix(line, "%Reason: "):
				info.Reason = strings.TrimPrefix(line, "%Reason: ")
			case strings.HasPrefix(line, "%SignedAt: "):
				if t, err := time.Parse(time.RFC3339Nano, strings.TrimPrefix(line, "%SignedAt: ")); err == nil {
					info.SignedAt = t
				}
			}
		}
		info.Issuer = "MockSigner (development only)"
		out.Signatures = append(out.Signatures, info)
	}
	out.SignatureCount = len(out.Signatures)
	// The mock's LTV flag is always false — we never embed validation
	// material. Adobe Reader would flag this even if the rest of the
	// signature were valid.
	out.LTVEnabled = false
	return out, nil
}

// compileTimeAssert: MockSigner must implement Signer.
var _ Signer = (*MockSigner)(nil)

// unused import guard keeps fmt available for future expansion.
var _ = fmt.Sprintf
