// ============================================================================
// SMOKE CHECK ONLY — NOT SUFFICIENT FOR PRODUCTION ACCEPTANCE.
//
// This validator regexes over raw PDF bytes. Regexes are not a PDF
// parser: they can match inside string objects, miss encrypted xref
// streams, and mis-identify signature dictionaries inside object
// streams. Use this package to catch *structural regressions* during
// CI; use Adobe Reader + EU DSS for cryptographic acceptance.
//
// Production acceptance gate: `make test-pades-strict` fails the
// build if any source file under //go:build prod_accept references
// this package. A follow-up ticket (T-D-7b) tracks replacing these
// regexes with a proper pdfcpu-backed parser; until that lands, the
// prod_accept gate is the invariant keeping this package out of the
// release-qualification path.
// ============================================================================
//
// Package pades is the CI-side PAdES structure validator for Wave
// 15.4 (and the broader Wave 9 signer sidecar DoD).
//
// Scope — what this validator DOES:
//   - Detects the number of `/Type /Sig` dictionaries in a PDF.
//   - Verifies the `/ByteRange` entries are well-formed and do NOT
//     cover the entire file (→ tamper-detectable).
//   - Asserts a `/DSS` dictionary exists (PAdES-B-LT requirement —
//     without the DSS, embedded validation material can't be
//     located offline).
//   - Asserts a Document Timestamp signature (`DocTimeStamp`)
//     exists (PAdES-B-LT requirement — this is the archival
//     timestamp).
//
// Scope — what this validator does NOT do:
//   - Cryptographic signature validation (chain, OCSP, CRL, TSA).
//     That requires a real signer + the EU DSS library; the
//     sidecar lives out of this repo. The harness operator
//     validates cryptographic conformance once per release via
//     Adobe Reader + EU DSS; this Go-side validator catches the
//     *structural* regressions between those full runs.
//
// The two checks combined — CI structural + periodic cryptographic —
// are what the Wave 9 / Wave 15.4 DoD calls for. Adobe Reader's
// green-tick is the gold standard; this package is what a Go
// developer runs on every PR so the gold standard doesn't regress
// silently between quarterly validations.
package pades

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
)

// Report is the outcome of a single validation run.
type Report struct {
	// SignatureCount is the number of `/Type /Sig` dictionaries.
	// A PAdES-B-LT envelope has at least one regular signature plus
	// one document timestamp → count ≥ 2.
	SignatureCount int
	// DocumentTimestamps is the subset of SignatureCount that are
	// `/SubFilter /ETSI.RFC3161` — the PAdES archival timestamp.
	DocumentTimestamps int
	// HasDSS is true when the PDF carries a /DSS catalog entry.
	HasDSS bool
	// ByteRangeCoversWholeFile is a tamper-detection alarm — when
	// true, the signature byte range equals the file length and
	// there's no window to detect modification. This is the red
	// flag the DoD's "tamper-evident" clause requires be absent.
	ByteRangeCoversWholeFile bool
	// ByteRanges captures the parsed `/ByteRange [a b c d]` tuples
	// for downstream audit.
	ByteRanges []ByteRange
	// Diagnostics carries any per-check notes — surfaced to the
	// operator when the run fails.
	Diagnostics []string
}

// ByteRange is the standard (offset, length, offset, length) shape
// PAdES uses to carve the signed bytes out of the PDF.
type ByteRange struct {
	Offset1, Length1, Offset2, Length2 int
}

// Total returns the total number of bytes the range covers.
func (b ByteRange) Total() int { return b.Length1 + b.Length2 }

// ValidatePAdESBLT runs the structural checks and returns a Report.
// An error is returned only when the PDF is unparseable; otherwise
// the caller inspects `Report` fields + `IsValidPAdESBLT()`.
func ValidatePAdESBLT(pdf []byte) (*Report, error) {
	if len(pdf) < 32 {
		return nil, fmt.Errorf("pades: input too short (%d bytes) to be a PDF", len(pdf))
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		return nil, fmt.Errorf("pades: missing %%PDF- header")
	}

	rep := &Report{}
	rep.SignatureCount = countOccurrences(pdf, sigDictRE)
	rep.DocumentTimestamps = countOccurrences(pdf, docTimestampRE)
	rep.HasDSS = dssRE.Match(pdf)

	ranges, err := parseByteRanges(pdf)
	if err != nil {
		return nil, err
	}
	rep.ByteRanges = ranges
	for _, br := range ranges {
		if br.Total() >= len(pdf) {
			rep.ByteRangeCoversWholeFile = true
			rep.Diagnostics = append(rep.Diagnostics,
				fmt.Sprintf("byte range %d+%d covers entire file (%d bytes) — no tamper-detection window",
					br.Length1, br.Length2, len(pdf)))
			break
		}
	}
	return rep, nil
}

// IsValidPAdESBLT reports whether the report satisfies the
// structural PAdES-B-LT profile. Does NOT assert the signatures are
// cryptographically valid — see package docstring.
func (r *Report) IsValidPAdESBLT() bool {
	return r.SignatureCount >= 1 &&
		r.DocumentTimestamps >= 1 &&
		r.HasDSS &&
		!r.ByteRangeCoversWholeFile
}

// ---- internals ------------------------------------------------------------

// The PDF matches use regex on the raw bytes. The signature /Sig
// dictionary is the anchor we count — it's the one element every
// signature + timestamp shares. We scan the whole file rather than
// parsing the xref because intermediate updates (common with
// incremental PAdES revisions) leave orphaned dict objects the
// xref doesn't always catch.
var (
	sigDictRE       = regexp.MustCompile(`/Type\s*/Sig\b`)
	docTimestampRE  = regexp.MustCompile(`/SubFilter\s*/ETSI\.RFC3161`)
	dssRE           = regexp.MustCompile(`/DSS\b`)
	byteRangeRE     = regexp.MustCompile(`/ByteRange\s*\[\s*(\d+)\s+(\d+)\s+(\d+)\s+(\d+)\s*\]`)
)

func countOccurrences(data []byte, re *regexp.Regexp) int {
	return len(re.FindAllIndex(data, -1))
}

func parseByteRanges(pdf []byte) ([]ByteRange, error) {
	matches := byteRangeRE.FindAllSubmatch(pdf, -1)
	out := make([]ByteRange, 0, len(matches))
	for _, m := range matches {
		if len(m) != 5 {
			continue
		}
		br, err := parseBRMatch(m)
		if err != nil {
			return nil, err
		}
		out = append(out, br)
	}
	return out, nil
}

func parseBRMatch(m [][]byte) (ByteRange, error) {
	nums := make([]int, 4)
	for i := 1; i < 5; i++ {
		n, err := strconv.Atoi(string(m[i]))
		if err != nil {
			return ByteRange{}, fmt.Errorf("pades: bad byte-range int %q: %w", m[i], err)
		}
		nums[i-1] = n
	}
	return ByteRange{nums[0], nums[1], nums[2], nums[3]}, nil
}
