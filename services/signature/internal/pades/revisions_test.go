package pades

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Fixtures in testdata/ are real output of the DSS sidecar's
// SignerService (services/signature-signer: DSS 6.3 / PDFBox 3, a
// self-signed test key, no TSA so PAdES-B-B):
//
//   - ceremony-3sig.pdf: base.pdf signed three times in sequence, the way
//     SealCeremony chains signer revisions and the organizational seal.
//   - tampered-between-sigs.pdf: base.pdf signed once, then an
//     incremental update that rewrites the page content stream
//     ("Pay 100 EUR" → "Pay 900 EUR"), then signed again.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func validate(t *testing.T, pdf []byte) *Report {
	t.Helper()
	rep, err := NewVerifier(VerifierOptions{SkipNetworkLookups: true}).Validate(context.Background(), pdf)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	return rep
}

// sigsByCoverage returns the report's signatures ordered by how far their
// ByteRange reaches (signing order).
func sigsByCoverage(t *testing.T, pdf []byte, rep *Report) []SignatureInfo {
	t.Helper()
	doc, err := parsePDF(pdf)
	if err != nil {
		t.Fatal(err)
	}
	idx := make([]int, len(doc.Signatures))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool {
		ra, rb := doc.Signatures[idx[a]].ByteRange, doc.Signatures[idx[b]].ByteRange
		return ra[2]+ra[3] < rb[2]+rb[3]
	})
	out := make([]SignatureInfo, len(idx))
	for i, j := range idx {
		out[i] = rep.Signatures[j]
	}
	return out
}

var startxrefTestRE = regexp.MustCompile(`startxref\s+(\d+)`)
var sizeTestRE = regexp.MustCompile(`/Size\s+(\d+)`)

// appendIncrement writes a well-formed incremental update: the given
// objects (number → body between "obj" and "endobj"), an xref table with
// one subsection per object, extra xref lines, and a trailer chained
// with /Prev. trailerRoot is the /Root reference to write.
func appendIncrement(pdf []byte, objs map[int]string, extraXref, trailerRoot string) []byte {
	var b bytes.Buffer
	b.Write(pdf)
	if !bytes.HasSuffix(pdf, []byte("\n")) {
		b.WriteByte('\n')
	}
	nums := make([]int, 0, len(objs))
	for n := range objs {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	offs := map[int]int{}
	size := 0
	if m := sizeTestRE.FindAllSubmatch(pdf, -1); m != nil {
		fmt.Sscanf(string(m[len(m)-1][1]), "%d", &size)
	}
	for _, n := range nums {
		offs[n] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", n, objs[n])
		if n+1 > size {
			size = n + 1
		}
	}
	xref := b.Len()
	b.WriteString("xref\n0 1\n0000000000 65535 f \n")
	for _, n := range nums {
		fmt.Fprintf(&b, "%d 1\n%010d 00000 n \n", n, offs[n])
	}
	b.WriteString(extraXref)
	m := startxrefTestRE.FindAllSubmatch(pdf, -1)
	prev := string(m[len(m)-1][1])
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root %s /Prev %s >>\nstartxref\n%d\n%%%%EOF\n", size, trailerRoot, prev, xref)
	return b.Bytes()
}

// ceremonyCatalog is the latest catalog of ceremony-3sig.pdf.
const ceremonyCatalog = "<< /Type /Catalog /Pages 2 0 R /AcroForm << /Fields [6 0 R 10 0 R 14 0 R] /SigFlags 3 >>"

func requireAllTamperEvident(t *testing.T, rep *Report) {
	t.Helper()
	for i, s := range rep.Signatures {
		if !s.TamperEvident || contains(s.Errors, "bytes_after_signature") {
			t.Errorf("signature %d (%s): TamperEvident=%v errors=%v", i, s.SignerName, s.TamperEvident, s.Errors)
		}
		// Only the chain may fail: the fixture key is self-signed.
		for _, e := range s.Errors {
			if !strings.HasPrefix(e, "chain_unverifiable") {
				t.Errorf("signature %d: unexpected error %q", i, e)
			}
		}
	}
	if !rep.TamperEvident {
		t.Errorf("document TamperEvident=false; errors=%v", rep.Errors)
	}
}

func TestVerify_MultiRevisionCeremonyTamperEvident(t *testing.T) {
	ceremony := readFixture(t, "ceremony-3sig.pdf")

	t.Run("ceremony output is tamper evident", func(t *testing.T) {
		rep := validate(t, ceremony)
		if rep.SignatureCount != 3 {
			t.Fatalf("SignatureCount = %d, want 3", rep.SignatureCount)
		}
		requireAllTamperEvident(t, rep)
	})

	t.Run("DSS-only update after the last signature is allowed", func(t *testing.T) {
		pdf := appendIncrement(ceremony, map[int]string{
			1:  ceremonyCatalog + " /DSS 18 0 R >>",
			18: "<< /Type /DSS /Certs [19 0 R] >>",
			19: "<< /Length 4 >>\nstream\nCERT\nendstream",
		}, "", "1 0 R")
		requireAllTamperEvident(t, validate(t, pdf))
	})

	t.Run("content rewritten between signatures", func(t *testing.T) {
		pdf := readFixture(t, "tampered-between-sigs.pdf")
		rep := validate(t, pdf)
		sigs := sigsByCoverage(t, pdf, rep)
		if len(sigs) != 2 {
			t.Fatalf("signatures = %d, want 2", len(sigs))
		}
		if sigs[0].TamperEvident || !contains(sigs[0].Errors, "bytes_after_signature") {
			t.Errorf("first signature must flag the content rewrite after it: %+v", sigs[0])
		}
		if !sigs[1].TamperEvident {
			t.Errorf("second signature covers the whole file: %+v", sigs[1])
		}
		if rep.TamperEvident {
			t.Error("document must not be tamper evident")
		}
	})

	for name, pdf := range map[string][]byte{
		"trailing junk": append(append([]byte{}, ceremony...), []byte("\nTAMPERED\n")...),
		"content stream redefined": appendIncrement(ceremony, map[int]string{
			4: "<< /Length 5 >>\nstream\nHELLO\nendstream",
		}, "", "1 0 R"),
		"catalog swaps the page tree": appendIncrement(ceremony, map[int]string{
			1:  "<< /Type /Catalog /Pages 18 0 R /AcroForm << /Fields [6 0 R 10 0 R 14 0 R] /SigFlags 3 >> >>",
			18: "<< /Type /Pages /Kids [] /Count 0 >>",
		}, "", "1 0 R"),
		"trailer points at a new catalog": appendIncrement(ceremony, map[int]string{
			18: "<< /Type /Catalog /Pages 19 0 R >>",
			19: "<< /Type /Pages /Kids [] /Count 0 >>",
		}, "", "18 0 R"),
		"non-signature annotation added to the page": appendIncrement(ceremony, map[int]string{
			3: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] /Resources << /Font << /F1 5 0 R >> >> " +
				"/Contents 4 0 R /Annots [6 0 R 10 0 R 14 0 R 18 0 R] >>",
			18: "<< /Type /Annot /Subtype /FreeText /Rect [0 0 300 200] /Contents (Pay 900 EUR) >>",
		}, "", "1 0 R"),
		"page media box changed": appendIncrement(ceremony, map[int]string{
			3: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 30 20] /Resources << /Font << /F1 5 0 R >> >> " +
				"/Contents 4 0 R /Annots [6 0 R 10 0 R 14 0 R] >>",
		}, "", "1 0 R"),
		"signature field removed": appendIncrement(ceremony, map[int]string{
			1: "<< /Type /Catalog /Pages 2 0 R /AcroForm << /Fields [10 0 R 14 0 R] /SigFlags 3 >> >>",
		}, "", "1 0 R"),
		"object freed in the xref": appendIncrement(ceremony, map[int]string{
			18: "<< /Type /DSS >>",
		}, "4 1\n0000000000 00001 f \n", "1 0 R"),
		"xref entry re-points into the signed revision": appendIncrement(ceremony, map[int]string{
			18: "<< /Type /DSS >>",
		}, "4 1\n0000000009 00000 n \n", "1 0 R"),
	} {
		t.Run(name, func(t *testing.T) {
			rep := validate(t, pdf)
			if rep.TamperEvident {
				t.Fatal("document TamperEvident=true, want false")
			}
			sigs := sigsByCoverage(t, pdf, rep)
			last := sigs[len(sigs)-1]
			if last.TamperEvident || !contains(last.Errors, "bytes_after_signature") {
				t.Fatalf("last signature must flag the update: %+v", last)
			}
		})
	}
}

// The gap between the two signed ranges must be exactly the signature's
// own /Contents hex string; anything else in the gap is unsigned content.
func TestVerify_ByteRangeGapMustBeContents(t *testing.T) {
	ceremony := readFixture(t, "ceremony-3sig.pdf")
	doc, err := parsePDF(ceremony)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range doc.Signatures {
		if !gapIsContents(ceremony, &s) {
			t.Fatalf("real DSS signature gap rejected: %v", s.ByteRange)
		}
	}

	head := "%PDF-1.7\n1 0 obj <</Type /Sig /ByteRange [0 0000 00000 0000] /Contents <abcdef>>> endobj\n"
	tail := "2 0 obj (unsigned text) endobj\n%%EOF\n"
	pdf := []byte(head + tail)
	lt := bytes.IndexByte(pdf, '<') // the dict opener
	lt = bytes.Index(pdf[lt+2:], []byte("<")) + lt + 2
	gapEnd := len(head) + len("2 0 obj (unsigned text) endobj\n") // gap swallows object 2
	br := fmt.Sprintf("[0 %04d %05d %04d]", lt, gapEnd, len(pdf)-gapEnd)
	pdf = bytes.Replace(pdf, []byte("[0 0000 00000 0000]"), []byte(br), 1)

	rep, _ := NewVerifier(VerifierOptions{SkipNetworkLookups: true}).Validate(context.Background(), pdf)
	if rep == nil || len(rep.Signatures) != 1 {
		t.Fatalf("report: %+v", rep)
	}
	if rep.Signatures[0].TamperEvident || !contains(rep.Signatures[0].Errors, "byte_range_gap_mismatch") {
		t.Fatalf("a ByteRange gap wider than /Contents must be flagged: %+v", rep.Signatures[0])
	}
}

// obj4 returns the dict and body of the page content stream (object 4).
func obj4(t *testing.T, pdf []byte) (string, string) {
	t.Helper()
	m := regexp.MustCompile(`(?s)\n4 0 obj\s*(<<.*?>>)\s*stream\r?\n(.*?)\r?\nendstream`).FindSubmatch(pdf)
	if m == nil {
		t.Fatal("no object 4 stream in fixture")
	}
	return string(m[1]), string(m[2])
}

func requireTampered(t *testing.T, pdf []byte) {
	t.Helper()
	rep := validate(t, pdf)
	if rep.TamperEvident {
		t.Fatal("document TamperEvident=true, want false")
	}
	for i, s := range rep.Signatures {
		if s.TamperEvident || !contains(s.Errors, "bytes_after_signature") {
			t.Errorf("signature %d must flag the update: %+v", i, s)
		}
	}
}

// Review round 1 (reproduced bypass): a content stream redefined with a
// /Fields key was routed to the AcroForm rule, which compared dict keys
// only, so the rewritten page text passed.
func TestVerify_ContentStreamRewriteNotTamperEvident(t *testing.T) {
	ceremony := readFixture(t, "ceremony-3sig.pdf")
	dict, body := obj4(t, ceremony)
	rewritten := strings.Replace(body, "100", "900", 1)
	if rewritten == body {
		t.Fatal("fixture content stream does not contain 100")
	}
	withFields := strings.TrimSuffix(strings.TrimSpace(dict), ">>") + " /Fields [] >>"
	for name, obj := range map[string]string{
		"same dict":      dict + "\nstream\n" + rewritten + "\nendstream",
		"dict + /Fields": withFields + "\nstream\n" + rewritten + "\nendstream",
		"dict + /Fields, length kept": strings.TrimSuffix(strings.TrimSpace(dict), ">>") + " /Fields [] /SigFlags 3 >>" +
			"\nstream\n" + rewritten + "\nendstream",
	} {
		t.Run(name, func(t *testing.T) {
			requireTampered(t, appendIncrement(ceremony, map[int]string{4: obj}, "", "1 0 R"))
		})
	}
}

// An untyped stream is never the AcroForm, whatever keys it carries; only
// the object the catalog names as /AcroForm is.
func TestVerify_UntypedStreamWithFieldsKeyRejected(t *testing.T) {
	ceremony := readFixture(t, "ceremony-3sig.pdf")
	dict, body := obj4(t, ceremony)
	// Body unchanged: the shape alone must not make it an AcroForm.
	withFields := strings.TrimSuffix(strings.TrimSpace(dict), ">>") + " /Fields [] >>"
	requireTampered(t, appendIncrement(ceremony, map[int]string{4: withFields + "\nstream\n" + body + "\nendstream"}, "", "1 0 R"))

	// An untyped non-stream dict with /Fields that the catalog does not
	// reference: also not the AcroForm.
	requireTampered(t, appendIncrement(ceremony, map[int]string{
		5: "<< /Type /Font /Subtype /Type1 /BaseFont /Courier /Fields [] >>",
	}, "", "1 0 R"))

	// The real AcroForm moved to an indirect object the catalog names is
	// still checked (and accepted) under the AcroForm rules.
	step1 := appendIncrement(ceremony, map[int]string{
		1:  "<< /Type /Catalog /Pages 2 0 R /AcroForm 18 0 R >>",
		18: "<< /Fields [6 0 R 10 0 R 14 0 R] /SigFlags 3 >>",
	}, "", "1 0 R")
	requireAllTamperEvident(t, validate(t, step1))
	step2 := appendIncrement(step1, map[int]string{
		18: "<< /Fields [6 0 R 10 0 R 14 0 R 19 0 R] /SigFlags 3 >>",
		19: "<< /FT /Sig /Type /Annot /Subtype /Widget /T (Signature4) /Rect [0 0 0 0] /F 132 /P 3 0 R >>",
	}, "", "1 0 R")
	requireAllTamperEvident(t, validate(t, step2))
	step3 := appendIncrement(step1, map[int]string{
		18: "<< /Fields [6 0 R 10 0 R 14 0 R] /SigFlags 3 /NeedAppearances true >>",
	}, "", "1 0 R")
	requireTampered(t, step3)
}

// Review round 1 (reproduced bypass): a signed field (with /V) could be
// redefined with a new appearance, rectangle and flags as long as /V was
// kept — a visible overlay on top of a signed document.
func TestVerify_SignedFieldAppearanceOverlayRejected(t *testing.T) {
	ceremony := readFixture(t, "ceremony-3sig.pdf")
	requireTampered(t, appendIncrement(ceremony, map[int]string{
		6: "<< /FT /Sig /Type /Annot /Subtype /Widget /F 4 /T (Signature1) /V 7 0 R /P 3 0 R " +
			"/Rect [0 0 300 200] /AP << /N 18 0 R >> >>",
		18: "<< /Type /XObject /Subtype /Form /BBox [0 0 300 200] /Length 5 >>\nstream\nHELLO\nendstream",
	}, "", "1 0 R"))
	// Only /F changed.
	requireTampered(t, appendIncrement(ceremony, map[int]string{
		6: "<< /FT /Sig /Type /Annot /Subtype /Widget /F 4 /T (Signature1) /V 7 0 R /P 3 0 R " +
			"/Rect [0.0 0.0 0.0 0.0] /AP 8 0 R >>",
	}, "", "1 0 R"))
	// Re-emitting the signed field unchanged (what PDFBox 3 does) stays fine.
	requireAllTamperEvident(t, validate(t, appendIncrement(ceremony, map[int]string{
		6: "<< /FT /Sig /Type /Annot /Subtype /Widget /F 132 /T (Signature1) /V 7 0 R /P 3 0 R " +
			"/Rect [0.0 0.0 0.0 0.0] /AP 8 0 R >>",
	}, "", "1 0 R")))
}

// The first signed range must start at byte 0; otherwise the bytes before
// it (the PDF header, the first objects) are unsigned even when the gap is
// exactly /Contents.
func TestVerify_ByteRangeMustStartAtZero(t *testing.T) {
	head := "%PDF-1.7\n1 0 obj <</Type /Sig /ByteRange [0000 0000 00000 0000] /Contents <abcdef>>> endobj\n"
	pdf := []byte(head + "%%EOF\n")
	lt := bytes.Index(pdf, []byte("/Contents <")) + len("/Contents ")
	gt := bytes.Index(pdf[lt:], []byte(">")) + lt + 1
	br := fmt.Sprintf("[%04d %04d %05d %04d]", 9, lt-9, gt, len(pdf)-gt)
	pdf = bytes.Replace(pdf, []byte("[0000 0000 00000 0000]"), []byte(br), 1)

	doc, err := parsePDF(pdf)
	if err != nil || len(doc.Signatures) != 1 {
		t.Fatalf("parse: %v (%d sigs)", err, len(doc.Signatures))
	}
	if gapIsContents(pdf, &doc.Signatures[0]) {
		t.Fatalf("ByteRange %v leaves the first 9 bytes unsigned", doc.Signatures[0].ByteRange)
	}
	rep, _ := NewVerifier(VerifierOptions{SkipNetworkLookups: true}).Validate(context.Background(), pdf)
	if rep.Signatures[0].TamperEvident || !contains(rep.Signatures[0].Errors, "byte_range_gap_mismatch") {
		t.Fatalf("want byte_range_gap_mismatch: %+v", rep.Signatures[0])
	}
}

// testVRIKey is a syntactically valid VRI key (hex SHA-1 of a signature).
const testVRIKey = "/0123456789ABCDEF0123456789ABCDEF01234567"

// Review round 2 (reproduced bypass): the objects a later revision may
// rewrite as /DSS material were collected from the catalog in force at
// the END of the file, i.e. the attacker's own catalog. Naming a new
// /DSS that lists a signed object made that object rewritable. Only the
// catalog in force before the update decides what is DSS material.
func TestVerify_DSSReachabilityPoisoningRejected(t *testing.T) {
	ceremony := readFixture(t, "ceremony-3sig.pdf")
	dict, body := obj4(t, ceremony)
	rewritten := dict + "\nstream\n" + strings.Replace(body, "100", "900", 1) + "\nendstream"
	overlay := "<< /Type /XObject /Subtype /Form /BBox [0 0 300 200] /Length 5 >>\nstream\nHELLO\nendstream"
	for name, objs := range map[string]map[int]string{
		// The reviewer's probe: page text "Pay 100 EUR" → "Pay 900 EUR".
		"new /DSS lists the page content as /Certs": {
			1:  ceremonyCatalog + " /DSS 18 0 R >>",
			18: "<< /Type /DSS /Certs [4 0 R] >>",
			4:  rewritten,
		},
		"new /VRI lists the page content as /Cert": {
			1:  ceremonyCatalog + " /DSS 18 0 R >>",
			18: "<< /Type /DSS /VRI 19 0 R >>",
			19: "<< " + testVRIKey + " 20 0 R >>",
			20: "<< /Cert [4 0 R] >>",
			4:  rewritten,
		},
		// Object 8 is signature 1's (untyped) appearance dictionary.
		"catalog names a signed appearance dict as /DSS": {
			1:  ceremonyCatalog + " /DSS 8 0 R >>",
			8:  "<< /N 18 0 R >>",
			18: overlay,
		},
		"new /DSS names a signed appearance dict as /VRI": {
			1:  ceremonyCatalog + " /DSS 18 0 R >>",
			18: "<< /Type /DSS /VRI 8 0 R >>",
			8:  "<< " + testVRIKey + " 19 0 R >>",
			19: "<< /Cert [] >>",
		},
	} {
		t.Run(name, func(t *testing.T) {
			requireTampered(t, appendIncrement(ceremony, objs, "", "1 0 R"))
		})
	}
}

// A /DSS added or changed after a signature may reference only objects
// that are new in the update, or that the /DSS in force before it already
// listed (DSS reuses known certificate/OCSP/CRL objects when it extends
// to B-LTA). Pointing it at any other signed object is refused even when
// that object is not rewritten.
func TestVerify_NewDSSMayNotReferencePriorObjects(t *testing.T) {
	ceremony := readFixture(t, "ceremony-3sig.pdf")
	for name, objs := range map[string]map[int]string{
		"/Certs names the page content": {
			1:  ceremonyCatalog + " /DSS 18 0 R >>",
			18: "<< /Type /DSS /Certs [19 0 R 4 0 R] >>",
			19: "<< /Length 4 >>\nstream\nCERT\nendstream",
		},
		"/OCSPs names a signature dict": {
			1:  ceremonyCatalog + " /DSS 18 0 R >>",
			18: "<< /Type /DSS /OCSPs [7 0 R] >>",
		},
		"/VRI names a signed appearance dict": {
			1:  ceremonyCatalog + " /DSS 18 0 R >>",
			18: "<< /Type /DSS /VRI 8 0 R >>",
		},
		"VRI entry /TS names the first signature dict": {
			1:  ceremonyCatalog + " /DSS 18 0 R >>",
			18: "<< /Type /DSS /VRI << " + testVRIKey + " << /TS 7 0 R >> >> >>",
		},
		"inline /DSS names the page content": {
			1: ceremonyCatalog + " /DSS << /Type /DSS /CRLs [4 0 R] >> >>",
		},
	} {
		t.Run(name, func(t *testing.T) {
			requireTampered(t, appendIncrement(ceremony, objs, "", "1 0 R"))
		})
	}

	// A signature made after a first DSS update: stage1 is where it would
	// end, so from = len(stage1).
	stage1 := appendIncrement(ceremony, map[int]string{
		1:  ceremonyCatalog + " /DSS 18 0 R >>",
		18: "<< /Type /DSS /Certs [19 0 R] >>",
		19: "<< /Length 4 >>\nstream\nCERT\nendstream",
	}, "", "1 0 R")
	check := func(objs map[int]string) error {
		t.Helper()
		pdf := appendIncrement(stage1, objs, "", "1 0 R")
		doc, err := parsePDF(pdf)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		return laterRevisionsAllowed(context.Background(), doc, len(stage1))
	}
	t.Run("new /DSS reuses the prior DSS certificate", func(t *testing.T) {
		if err := check(map[int]string{
			1:  ceremonyCatalog + " /DSS 20 0 R >>",
			20: "<< /Type /DSS /Certs [19 0 R 21 0 R] >>",
			21: "<< /Length 5 >>\nstream\nCERT2\nendstream",
		}); err != nil {
			t.Fatalf("want allowed, got %v", err)
		}
	})
	t.Run("prior /DSS extended in place", func(t *testing.T) {
		if err := check(map[int]string{
			18: "<< /Type /DSS /Certs [19 0 R 20 0 R] >>",
			20: "<< /Length 5 >>\nstream\nCERT2\nendstream",
		}); err != nil {
			t.Fatalf("want allowed, got %v", err)
		}
	})
	t.Run("new /DSS also names the page content", func(t *testing.T) {
		if err := check(map[int]string{
			1:  ceremonyCatalog + " /DSS 20 0 R >>",
			20: "<< /Type /DSS /Certs [19 0 R 4 0 R] >>",
		}); err == nil {
			t.Fatal("want refused: object 4 was never DSS material")
		}
	})
	t.Run("prior /DSS extended with the page content", func(t *testing.T) {
		if err := check(map[int]string{
			18: "<< /Type /DSS /Certs [19 0 R 4 0 R] >>",
		}); err == nil {
			t.Fatal("want refused: object 4 was never DSS material")
		}
	})
}

// testVRIKey2 is a second syntactically valid VRI key.
const testVRIKey2 = "/89ABCDEF89ABCDEF89ABCDEF89ABCDEF89ABCDEF"

// Review round 3 (shadow-attack threat model): the attacker may author the
// document the victim signs, so whatever the signed revision's catalog
// names as DSS material or XMP metadata is attacker-chosen. No allowance
// derived from it may permit rewriting a stream or an arbitrary dict:
// every redefined stream must be re-emitted unchanged, a signed /Metadata
// stream is never redefined, and DSS/VRI dicts may only grow by references
// to new objects.
func TestVerify_PreSigningPoisonedAllowancesRejected(t *testing.T) {
	ceremony := readFixture(t, "ceremony-3sig.pdf")
	dict, body := obj4(t, ceremony)
	rewritten := strings.Replace(body, "100", "900", 1)
	open := strings.TrimSuffix(strings.TrimSpace(dict), ">>")
	stream := func(d, b string) string { return d + "\nstream\n" + b + "\nendstream" }
	metaDict := open + " /Type /Metadata /Subtype /XML >>"
	overlay := "<< /Type /XObject /Subtype /Form /BBox [0 0 300 200] /Length 5 >>\nstream\nHELLO\nendstream"
	cert := func(s string) string { return fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(s), s) }
	xmp := func(s string) string {
		return fmt.Sprintf("<< /Type /Metadata /Subtype /XML /Length %d >>\nstream\n%s\nendstream", len(s), s)
	}
	// A signed revision with a well-formed DSS: certificate 19, VRI map 20
	// with one entry 21.
	dssBase := map[int]string{
		1:  ceremonyCatalog + " /DSS 18 0 R >>",
		18: "<< /Type /DSS /Certs [19 0 R] /VRI 20 0 R >>",
		19: cert("CERT"),
		20: "<< " + testVRIKey + " 21 0 R >>",
		21: "<< /Cert [19 0 R] >>",
	}
	metaBase := map[int]string{1: ceremonyCatalog + " /Metadata 18 0 R >>", 18: xmp("<x>A</x>")}

	// check runs the revision checker as if a signature covered exactly
	// `signed` (ceremony plus one attacker-authored update) and `after`
	// was appended later.
	check := func(t *testing.T, signed, after map[int]string) error {
		t.Helper()
		stage1 := appendIncrement(ceremony, signed, "", "1 0 R")
		doc, err := parsePDF(appendIncrement(stage1, after, "", "1 0 R"))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		return laterRevisionsAllowed(context.Background(), doc, len(stage1))
	}

	for name, c := range map[string]struct{ signed, after map[int]string }{
		"R3a signed /DSS lists the content stream, then it is rewritten": {
			map[int]string{1: ceremonyCatalog + " /DSS 18 0 R >>", 18: "<< /Type /DSS /Certs [4 0 R] >>"},
			map[int]string{4: stream(dict, rewritten)},
		},
		"R3b signed /Metadata is the content stream, then it is rewritten": {
			map[int]string{1: ceremonyCatalog + " /Metadata 4 0 R >>", 4: stream(metaDict, body)},
			map[int]string{4: stream(metaDict, rewritten)},
		},
		"R3c signed /DSS is an appearance dict, then it is swapped": {
			map[int]string{1: ceremonyCatalog + " /DSS 8 0 R >>"},
			map[int]string{8: "<< /N 18 0 R >>", 18: overlay},
		},
		"signed /DSS is the content stream, re-emitted with a /Filter": {
			map[int]string{1: ceremonyCatalog + " /DSS 4 0 R >>"},
			map[int]string{4: stream(open+" /Filter /ASCIIHexDecode >>", body)},
		},
		"signed /DSS is the content stream, re-emitted with /DecodeParms": {
			map[int]string{1: ceremonyCatalog + " /DSS 4 0 R >>"},
			map[int]string{4: stream(open+" /DecodeParms << /Predictor 2 >> >>", body)},
		},
		"signed XMP metadata redefined in place": {
			metaBase, map[int]string{18: xmp("<x>B</x>")},
		},
		"signed DSS certificate stream rewritten": {
			dssBase, map[int]string{19: cert("CERX")},
		},
		"signed /DSS drops a certificate": {
			dssBase, map[int]string{18: "<< /Type /DSS /Certs [] /VRI 20 0 R >>"},
		},
		"signed /DSS gains a non-DSS key": {
			dssBase, map[int]string{18: "<< /Type /DSS /Certs [19 0 R] /VRI 20 0 R /N 9 0 R >>"},
		},
		"signed /DSS gains a signed object": {
			dssBase, map[int]string{18: "<< /Type /DSS /Certs [19 0 R 4 0 R] /VRI 20 0 R >>"},
		},
		"signed /DSS re-points its /VRI": {
			dssBase, map[int]string{18: "<< /Type /DSS /Certs [19 0 R] /VRI 22 0 R >>", 22: "<< >>"},
		},
		"signed VRI map re-points an entry": {
			dssBase, map[int]string{20: "<< " + testVRIKey + " 22 0 R >>", 22: "<< /Cert [19 0 R] >>"},
		},
		"signed VRI entry gains a signed object": {
			dssBase, map[int]string{21: "<< /Cert [19 0 R 4 0 R] >>"},
		},
		"signed VRI entry gains a non-VRI key": {
			dssBase, map[int]string{21: "<< /Cert [19 0 R] /N 9 0 R >>"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := check(t, c.signed, c.after); err == nil {
				t.Fatal("laterRevisionsAllowed = nil, want the update refused")
			}
		})
	}

	for name, c := range map[string]struct{ signed, after map[int]string }{
		"content stream re-emitted unchanged": {
			map[int]string{1: ceremonyCatalog + " /DSS 4 0 R >>"},
			map[int]string{4: stream(dict, body)},
		},
		"XMP replaced by a new object, catalog re-pointed": {
			metaBase, map[int]string{1: ceremonyCatalog + " /Metadata 22 0 R >>", 22: xmp("<x>B</x>")},
		},
		"signed /DSS extended with new objects": {
			dssBase, map[int]string{
				18: "<< /Type /DSS /Certs [19 0 R 22 0 R] /OCSPs [23 0 R] /VRI 20 0 R >>",
				22: cert("CERT2"), 23: cert("OCSP"),
			},
		},
		"signed VRI map gains an entry for a new signature": {
			dssBase, map[int]string{
				20: "<< " + testVRIKey + " 21 0 R " + testVRIKey2 + " 22 0 R >>",
				22: "<< /Cert [19 0 R] >>",
			},
		},
		"signed VRI entry extended with new objects": {
			dssBase, map[int]string{
				21: "<< /Cert [19 0 R] /OCSP [22 0 R] /TS 23 0 R /TU (D:20261012000000Z) >>",
				22: cert("OCSP"), 23: cert("TS"),
			},
		},
	} {
		t.Run("allowed: "+name, func(t *testing.T) {
			if err := check(t, c.signed, c.after); err != nil {
				t.Fatalf("laterRevisionsAllowed = %v, want nil", err)
			}
		})
	}
}

// Review round 4 (shadow model, /Length): the signed content stream hides
// operators behind a fake "endstream" that its /Length 42 lands on. A
// re-emission of the same bytes with a /Length that covers them (indirect,
// or direct but not landing on endstream) made bodies read "up to the
// first endstream" compare equal, so the hidden "Pay 900 EUR" passed. A
// redefined stream must now be byte-identical over its whole object span,
// with a direct /Length that lands exactly on endstream.
func TestVerify_StreamLengthReemitRejected(t *testing.T) {
	ceremony := readFixture(t, "ceremony-3sig.pdf")
	_, body := obj4(t, ceremony)
	if len(body) != 42 {
		t.Fatalf("fixture content stream is %d bytes, want 42", len(body))
	}
	raw4 := body + "\nendstream\n" + "BX BT /F1 18 Tf 20 60 Td (Pay 900 EUR) Tj ET EX"
	check := func(t *testing.T, signed, after map[int]string) error {
		t.Helper()
		stage1 := appendIncrement(ceremony, signed, "", "1 0 R")
		doc, err := parsePDF(appendIncrement(stage1, after, "", "1 0 R"))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		return laterRevisionsAllowed(context.Background(), doc, len(stage1))
	}
	hiding := map[int]string{4: "<< /Length 42 >>\nstream\n" + raw4 + "\nendstream"}

	for name, c := range map[string]struct{ signed, after map[int]string }{
		"R4 same bytes, indirect /Length": {hiding, map[int]string{
			4:  "<< /Length 18 0 R >>\nstream\n" + raw4 + "\nendstream",
			18: fmt.Sprintf("%d", len(raw4)),
		}},
		"R4b same bytes, direct /Length not landing on endstream": {hiding, map[int]string{
			4: fmt.Sprintf("<< /Length %d >>\nstream\n", len(raw4)-1) + raw4 + "\nendstream",
		}},
		"same bytes, direct /Length changed to the full body": {hiding, map[int]string{
			4: fmt.Sprintf("<< /Length %d >>\nstream\n", len(raw4)) + raw4 + "\nendstream",
		}},
		"signed stream has an indirect /Length, re-emitted unchanged": {
			map[int]string{4: "<< /Length 18 0 R >>\nstream\n" + body + "\nendstream", 18: "42"},
			map[int]string{4: "<< /Length 18 0 R >>\nstream\n" + body + "\nendstream"},
		},
		"signed stream /Length does not land, re-emitted unchanged": {
			map[int]string{4: "<< /Length 40 >>\nstream\n" + body + "\nendstream"},
			map[int]string{4: "<< /Length 40 >>\nstream\n" + body + "\nendstream"},
		},
		"dict key order changed": {
			map[int]string{4: "<< /Length 42 /Filter [] >>\nstream\n" + body + "\nendstream"},
			map[int]string{4: "<< /Filter [] /Length 42 >>\nstream\n" + body + "\nendstream"},
		},
		// Its /Length lands on the fake endstream, which is not followed by
		// endobj: the object is ambiguous, so even an identical re-emit is
		// refused (fail closed).
		"hiding stream re-emitted byte-identical": {hiding, hiding},
	} {
		t.Run(name, func(t *testing.T) {
			if err := check(t, c.signed, c.after); err == nil {
				t.Fatal("laterRevisionsAllowed = nil, want the update refused")
			}
		})
	}

	t.Run("allowed: well-formed stream re-emitted byte-identical", func(t *testing.T) {
		plain := map[int]string{4: "<< /Length 42 >>\nstream\n" + body + "\nendstream"}
		if err := check(t, plain, plain); err != nil {
			t.Fatalf("laterRevisionsAllowed = %v, want nil", err)
		}
	})
}

// Minor (review round 4): an in-place /DSS or VRI update may reuse objects
// the signed /DSS already lists (DSS does this when it extends to B-LTA),
// consistent with dssRefsAllowed; other signed objects stay refused.
func TestVerify_InPlaceDSSUpdateReusesDSSMaterial(t *testing.T) {
	ceremony := readFixture(t, "ceremony-3sig.pdf")
	cert := func(s string) string { return fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(s), s) }
	signed := map[int]string{
		1:  ceremonyCatalog + " /DSS 18 0 R >>",
		18: "<< /Type /DSS /Certs [19 0 R 22 0 R] /OCSPs [23 0 R] /VRI 20 0 R >>",
		19: cert("CERT"), 22: cert("CA"), 23: cert("OCSP"),
		20: "<< " + testVRIKey + " 21 0 R >>",
		21: "<< /Cert [19 0 R] >>",
	}
	stage1 := appendIncrement(ceremony, signed, "", "1 0 R")
	check := func(after map[int]string) error {
		t.Helper()
		doc, err := parsePDF(appendIncrement(stage1, after, "", "1 0 R"))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		return laterRevisionsAllowed(context.Background(), doc, len(stage1))
	}
	if err := check(map[int]string{
		21: "<< /Cert [19 0 R 22 0 R] /OCSP [23 0 R] /TS 24 0 R >>",
		24: cert("TS"),
	}); err != nil {
		t.Fatalf("VRI entry reusing DSS certificates: %v, want nil", err)
	}
	if err := check(map[int]string{
		18: "<< /Type /DSS /Certs [19 0 R 22 0 R 23 0 R] /OCSPs [23 0 R] /CRLs [19 0 R] /VRI 20 0 R >>",
	}); err != nil {
		t.Fatalf("/DSS arrays reusing DSS objects: %v, want nil", err)
	}
	if err := check(map[int]string{21: "<< /Cert [19 0 R 7 0 R] >>"}); err == nil {
		t.Fatal("a signature dict (never DSS material) must stay refused")
	}
}
