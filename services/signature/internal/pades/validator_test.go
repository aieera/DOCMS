package pades

// Unit tests with synthetic PDF payloads that contain just enough
// structure for the validator's regexes to hit. Real cryptographic
// validation happens in a separate harness (EU DSS + Adobe Reader);
// see the package docstring + docs/runbooks/15-pades-harness.md.

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

// synthPAdES returns a byte stream that passes the four structural
// checks: PDF header, one /Type /Sig, one DocTimeStamp (detected by
// the /SubFilter), a /DSS entry, and /ByteRange tuples that do NOT
// cover the whole file.
func synthPAdESBLT(size int) []byte {
	buf := bytes.Buffer{}
	buf.WriteString("%PDF-1.7\n")
	buf.WriteString("<< /Type /Sig /SubFilter /ETSI.CAdES.detached /ByteRange [0 100 200 50] >>\n")
	buf.WriteString("<< /Type /Sig /SubFilter /ETSI.RFC3161 /ByteRange [0 300 400 50] >>\n")
	buf.WriteString("<< /DSS << /Certs [] /CRLs [] >> >>\n")
	// Pad to requested size so ByteRange totals < file length.
	for buf.Len() < size {
		buf.WriteByte('x')
	}
	return buf.Bytes()
}

func TestValidator_RejectsShortInput(t *testing.T) {
	_, err := ValidatePAdESBLT([]byte("too short"))
	require.Error(t, err)
}

func TestValidator_RejectsNonPDF(t *testing.T) {
	_, err := ValidatePAdESBLT(bytes.Repeat([]byte("abcd"), 100))
	require.Error(t, err)
}

func TestValidator_DetectsSignaturesAndDSS(t *testing.T) {
	rep, err := ValidatePAdESBLT(synthPAdESBLT(10_000))
	require.NoError(t, err)
	require.Equal(t, 2, rep.SignatureCount, "two Sig dicts expected")
	require.Equal(t, 1, rep.DocumentTimestamps, "one DocTimeStamp expected")
	require.True(t, rep.HasDSS, "/DSS entry expected")
	require.False(t, rep.ByteRangeCoversWholeFile)
	require.True(t, rep.IsValidPAdESBLT(), "synthetic fixture should satisfy PAdES-B-LT: %+v", rep.Diagnostics)
}

func TestValidator_RejectsMissingDocTimestamp(t *testing.T) {
	pdf := []byte("%PDF-1.7\n<< /Type /Sig /SubFilter /ETSI.CAdES.detached /ByteRange [0 100 200 50] >>\n<< /DSS << >> >>\n")
	pdf = append(pdf, bytes.Repeat([]byte("x"), 10_000)...)
	rep, err := ValidatePAdESBLT(pdf)
	require.NoError(t, err)
	require.Equal(t, 0, rep.DocumentTimestamps)
	require.False(t, rep.IsValidPAdESBLT(), "no doc timestamp → fail")
}

func TestValidator_RejectsMissingDSS(t *testing.T) {
	pdf := []byte("%PDF-1.7\n<< /Type /Sig /SubFilter /ETSI.CAdES.detached /ByteRange [0 100 200 50] >>\n<< /Type /Sig /SubFilter /ETSI.RFC3161 /ByteRange [0 300 400 50] >>\n")
	pdf = append(pdf, bytes.Repeat([]byte("x"), 10_000)...)
	rep, err := ValidatePAdESBLT(pdf)
	require.NoError(t, err)
	require.False(t, rep.HasDSS)
	require.False(t, rep.IsValidPAdESBLT(), "no DSS → fail")
}

func TestValidator_FlagsByteRangeCoveringWholeFile(t *testing.T) {
	// Construct a small file whose byte-range sum equals its own
	// length. This is the tamper-evidence red flag the DoD names.
	body := "%PDF-1.7\n<< /Type /Sig /ByteRange [0 50 60 40] >>\n"
	// File length = 100; Length1+Length2 = 90. We want sum == len.
	for len(body) < 90 {
		body += "x"
	}
	rep, err := ValidatePAdESBLT([]byte(body))
	require.NoError(t, err)
	require.True(t, rep.ByteRangeCoversWholeFile,
		"byte-range covering file length should be flagged; diagnostics: %v", rep.Diagnostics)
	require.False(t, rep.IsValidPAdESBLT())
}

func TestValidator_ParsesMultipleByteRanges(t *testing.T) {
	rep, err := ValidatePAdESBLT(synthPAdESBLT(10_000))
	require.NoError(t, err)
	require.Len(t, rep.ByteRanges, 2)
	require.Equal(t, ByteRange{0, 100, 200, 50}, rep.ByteRanges[0])
	require.Equal(t, ByteRange{0, 300, 400, 50}, rep.ByteRanges[1])
}
