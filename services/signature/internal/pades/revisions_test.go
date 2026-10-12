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
		if !gapIsContents(ceremony, s) {
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
