package pades

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
)

// signatureBlock is one /Sig dict's parsed shape — enough for the
// CMS verifier to digest the right bytes and decode Contents.
type signatureBlock struct {
	// FieldName from the field dict the /Sig is attached to.
	// Optional (PDF allows unnamed sig fields).
	FieldName string
	// ByteRange = [start1 length1 start2 length2]. Positions cover
	// the bytes the CMS signed; the gap between them is /Contents.
	ByteRange [4]int64
	// Contents is the decoded hex string of the CMS SignedData.
	// Already trimmed of trailing zero padding PAdES adds to fix
	// the byte-range layout.
	Contents []byte
	// Reason / Location / Name / SubFilter from the /Sig dict.
	Reason   string
	Location string
	Name     string
	SubFilter string
	// SignedAt is the /M field, decoded from PDF "D:YYYYMMDDHHMMSS"
	// format. Optional — falls back to TSA timestamp if missing.
	SignedAtRaw string
	// Object number + generation, kept for traceability + DSS
	// embedding (the embedder needs to reference the /Sig object).
	ObjNum int
}

// dssBlock captures the document-level /DSS dict if present.
type dssBlock struct {
	// Certs / OCSPs / CRLs are object numbers of stream objects
	// holding raw cert / OCSP-response / CRL bytes.
	CertObjs []int
	OCSPObjs []int
	CRLObjs  []int
	// VRI maps the SHA-1 hex of a signature's /Contents → its
	// per-VRI dict (which itself references Cert/OCSP/CRL streams).
	VRI map[string]vriEntry
}

type vriEntry struct {
	CertObjs []int
	OCSPObjs []int
	CRLObjs  []int
}

// parsedDoc is the full result of parsing.
type parsedDoc struct {
	Bytes      []byte
	Signatures []signatureBlock
	DSS        *dssBlock
	Encrypted  bool
	HasObjStm  bool
	// Objects is a tiny xref view: object number → byte offset of
	// the `N M obj` line. We don't materialize objects unless asked.
	Objects map[int]int64
}

// parsePDF runs the byte-range + xref + sig-dict extraction.
// It's permissive: returns whatever it can locate even when the
// document has features we don't fully support, and surfaces those
// via the parsedDoc flags so the validator can decide whether to
// fail closed or proceed Tier-1-only.
func parsePDF(raw []byte) (*parsedDoc, error) {
	if len(raw) < 8 || !bytes.HasPrefix(raw, []byte("%PDF-")) {
		return nil, fmt.Errorf("%w: missing %%PDF- header", ErrParse)
	}
	doc := &parsedDoc{Bytes: raw, Objects: map[int]int64{}, Signatures: nil}

	// Detect encrypted docs early. /Encrypt in trailer means we
	// can't read the contents reliably without the password —
	// PAdES signed docs MUST be unencrypted by spec.
	if bytes.Contains(raw, []byte("/Encrypt ")) {
		doc.Encrypted = true
		return doc, ErrUnsupportedEncrypted
	}
	// Object-stream-compressed PDFs use /ObjStm. Real-world signed
	// PDFs stay flat by convention; bail with a structured error.
	if bytes.Contains(raw, []byte("/Type /ObjStm")) || bytes.Contains(raw, []byte("/Type/ObjStm")) {
		doc.HasObjStm = true
		return doc, ErrUnsupportedObjStream
	}

	// Walk the xref entries from every revision. We don't need a
	// fully reconstructed object graph — finding `N G obj` headers
	// and remembering their byte offsets is sufficient for sig
	// + DSS dict lookup.
	for _, m := range objHeaderRE.FindAllSubmatchIndex(raw, -1) {
		num, err := strconv.Atoi(string(raw[m[2]:m[3]]))
		if err != nil {
			continue
		}
		// Last seen offset wins → latest revision overrides.
		doc.Objects[num] = int64(m[0])
	}

	// Locate signature dicts via manual depth-walking — regex
	// chokes on the nested `<...>` hex string inside `<<...>>`.
	for _, span := range findSigDicts(raw) {
		blk, err := parseSigDict(raw, span[0], span[1])
		if err != nil {
			continue
		}
		doc.Signatures = append(doc.Signatures, *blk)
	}

	// Locate the document /DSS. Two shapes: (1) catalog-style
	// `/DSS X 0 R` reference, and (2) inline `/Type /DSS` dict
	// (used by some embedders + our test fixtures).
	if dssIdx := dssDictRE.FindIndex(raw); dssIdx != nil {
		doc.DSS = parseDSSDict(raw, dssIdx[0])
	} else if inlineIdx := dssTypeRE.FindIndex(raw); inlineIdx != nil {
		// Walk backward to the `<<` that opens this dict.
		start := inlineIdx[0]
		for start > 1 && string(raw[start-2:start]) != "<<" {
			start--
		}
		if start >= 2 {
			doc.DSS = parseDSSDict(raw, start-2)
		}
	}

	return doc, nil
}

// objHeaderRE matches `N G obj` at the start of a line.
var objHeaderRE = regexp.MustCompile(`(?m)^(\d+)\s+(\d+)\s+obj\b`)

// sigDictRE matches a `<<` opening that contains `/Type /Sig` (or
// /Type/Sig). It's loose on whitespace; PAdES dicts are usually
// pretty-printed but some signers omit space between names.
var sigDictRE = regexp.MustCompile(`<<[^>]*?/Type\s*/Sig\b[^>]*?>>`)

// dssDictRE: catalog-level /DSS dict.
var dssDictRE = regexp.MustCompile(`/DSS\s*<<[^>]*>>|/DSS\s+\d+\s+\d+\s+R`)

// parseSigDict decodes ByteRange + Contents + the small attribute
// set we surface up. Caller passes the full raw bytes + the
// dict's start/end offsets returned by sigDictRE.
func parseSigDict(raw []byte, dictStart, dictEnd int) (*signatureBlock, error) {
	dict := raw[dictStart:dictEnd]

	br := byteRangeRE.FindSubmatch(dict)
	if br == nil {
		return nil, errors.New("sig dict: ByteRange missing")
	}
	var rng [4]int64
	for i, s := range br[1:5] {
		v, err := strconv.ParseInt(string(s), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("byte range parse: %w", err)
		}
		rng[i] = v
	}

	contentsHex := contentsRE.FindSubmatch(dict)
	if contentsHex == nil {
		return nil, errors.New("sig dict: Contents missing")
	}
	// Strip the trailing zero padding PAdES adds to keep ByteRange
	// length stable. CMS is ASN.1 — extra zeros at the end aren't
	// part of any DER structure.
	hexBytes := bytes.TrimRight(contentsHex[1], "0 \t\n\r")
	if len(hexBytes)%2 != 0 {
		// Hex strings can have an odd-trailing zero we just trimmed.
		// PDF allows whitespace inside hex strings; restore one zero.
		hexBytes = append(hexBytes, '0')
	}
	contents := make([]byte, hex.DecodedLen(len(hexBytes)))
	n, err := hex.Decode(contents, hexBytes)
	if err != nil {
		return nil, fmt.Errorf("contents hex decode: %w", err)
	}
	contents = contents[:n]

	blk := &signatureBlock{ByteRange: rng, Contents: contents}
	blk.Reason = stringValue(dict, "Reason")
	blk.Location = stringValue(dict, "Location")
	blk.Name = stringValue(dict, "Name")
	blk.SubFilter = nameValue(dict, "SubFilter")
	blk.SignedAtRaw = stringValue(dict, "M")

	return blk, nil
}

// parseDSSDict extracts cert / OCSP / CRL object numbers + the
// /VRI map. Returns nil on parse failure (DSS is optional).
func parseDSSDict(raw []byte, start int) *dssBlock {
	end := start
	depth := 0
	for end < len(raw) {
		switch {
		case end+2 <= len(raw) && string(raw[end:end+2]) == "<<":
			depth++
			end += 2
		case end+2 <= len(raw) && string(raw[end:end+2]) == ">>":
			depth--
			end += 2
			if depth == 0 {
				goto done
			}
		default:
			end++
		}
	}
done:
	if end <= start {
		return nil
	}
	body := raw[start:end]
	d := &dssBlock{
		CertObjs: refArray(body, "Certs"),
		OCSPObjs: refArray(body, "OCSPs"),
		CRLObjs:  refArray(body, "CRLs"),
		VRI:      map[string]vriEntry{},
	}
	// /VRI parsing is intentionally minimal — most readers expect
	// per-signature entries keyed by SHA-1 hex of /Contents. We
	// surface what we find but tolerate a missing /VRI entirely.
	for _, m := range vriEntryRE.FindAllSubmatchIndex(body, -1) {
		key := string(body[m[2]:m[3]])
		entryBody := body[m[4]:m[5]]
		d.VRI[key] = vriEntry{
			CertObjs: refArray(entryBody, "Cert"),
			OCSPObjs: refArray(entryBody, "OCSP"),
			CRLObjs:  refArray(entryBody, "CRL"),
		}
	}
	return d
}

// refArray pulls a `/Name [ N M R ... ]` array of indirect
// references and returns the object numbers.
func refArray(in []byte, name string) []int {
	rx := regexp.MustCompile(`/` + name + `\s*\[([^\]]*)\]`)
	m := rx.FindSubmatch(in)
	if m == nil {
		return nil
	}
	tokens := refTokenRE.FindAllSubmatch(m[1], -1)
	out := make([]int, 0, len(tokens))
	for _, t := range tokens {
		n, err := strconv.Atoi(string(t[1]))
		if err == nil {
			out = append(out, n)
		}
	}
	return out
}

var (
	byteRangeRE = regexp.MustCompile(`/ByteRange\s*\[\s*(\d+)\s+(\d+)\s+(\d+)\s+(\d+)\s*\]`)
	contentsRE  = regexp.MustCompile(`/Contents\s*<([0-9A-Fa-f\s]+)>`)
	refTokenRE  = regexp.MustCompile(`(\d+)\s+\d+\s+R`)
	vriEntryRE  = regexp.MustCompile(`/([0-9A-Fa-f]{40,})\s*<<([^>]*)>>`)
	dssTypeRE   = regexp.MustCompile(`/Type\s*/DSS\b`)
	sigTypeRE   = regexp.MustCompile(`/Type\s*/Sig\b`)
)

// findSigDicts locates each `<< ... /Type /Sig ... >>` dict and
// returns [start,end) byte ranges into raw. Manual walk because
// PDF dicts contain `<...>` hex strings that confuse a regex
// approach: `[^>]*` stops at the hex's closing `>`, and `>>` can
// appear inside nested arrays.
//
// Algorithm: for each `/Type /Sig` hit, walk backward to the
// nearest `<<` that opens at depth 0; then walk forward
// matching `<<` / `>>` pairs (skipping `<...>` hex strings)
// until depth returns to 0.
func findSigDicts(raw []byte) [][2]int {
	var out [][2]int
	for _, m := range sigTypeRE.FindAllIndex(raw, -1) {
		hit := m[0]
		start := -1
		// Walk backward for the opening `<<`. PDF dicts are flat
		// at the object level; we just need the enclosing one.
		for i := hit - 2; i >= 0; i-- {
			if raw[i] == '<' && raw[i+1] == '<' {
				// Don't confuse with a hex string "<...>" — those
				// use single angle brackets, not doubled.
				start = i
				break
			}
			if raw[i] == '>' && i+1 < len(raw) && raw[i+1] == '>' {
				// Hit the END of a previous dict before finding
				// our opener — give up on this hit.
				break
			}
		}
		if start < 0 {
			continue
		}
		// Walk forward from start, tracking depth.
		depth := 0
		end := -1
		for i := start; i < len(raw)-1; i++ {
			switch {
			case raw[i] == '<' && raw[i+1] == '<':
				depth++
				i++
			case raw[i] == '>' && raw[i+1] == '>':
				depth--
				i++
				if depth == 0 {
					end = i + 1
				}
			case raw[i] == '<':
				// Hex string; skip to its closing single `>`.
				j := i + 1
				for j < len(raw) && raw[j] != '>' {
					j++
				}
				i = j
			}
			if end > 0 {
				break
			}
		}
		if end > start {
			out = append(out, [2]int{start, end})
		}
	}
	return out
}

// stringValue extracts a `/Name (literal string)` value. Returns ""
// if absent. Handles balanced parens minimally.
func stringValue(dict []byte, name string) string {
	rx := regexp.MustCompile(`/` + name + `\s*\(([^)]*)\)`)
	m := rx.FindSubmatch(dict)
	if m == nil {
		return ""
	}
	return string(m[1])
}

// nameValue extracts a `/Name /value` value.
func nameValue(dict []byte, name string) string {
	rx := regexp.MustCompile(`/` + name + `\s*/([A-Za-z0-9_.]+)`)
	m := rx.FindSubmatch(dict)
	if m == nil {
		return ""
	}
	return string(m[1])
}

// signedBytes returns the bytes covered by a signature's ByteRange.
// The CMS digest is computed over this; the gap (Contents) is what
// the signer signed *over*, not signed *with*.
func signedBytes(raw []byte, br [4]int64) ([]byte, error) {
	if br[0] < 0 || br[2] < 0 ||
		br[0]+br[1] > int64(len(raw)) ||
		br[2]+br[3] > int64(len(raw)) {
		return nil, fmt.Errorf("byte range out of bounds: %v vs len=%d", br, len(raw))
	}
	out := make([]byte, 0, br[1]+br[3])
	out = append(out, raw[br[0]:br[0]+br[1]]...)
	out = append(out, raw[br[2]:br[2]+br[3]]...)
	return out, nil
}

// extractStream returns the body of `N 0 obj << ... >> stream\n
// ...bytes... \nendstream endobj` for the given object number.
// Returns ErrParse-wrapped error when the object exists but isn't
// a stream (which is a valid PDF shape we just don't surface here),
// nil error + empty bytes when the object isn't found at all.
//
// We intentionally don't apply any /Filter — PDF DSS streams are
// stored uncompressed by every signer we've inspected (Adobe,
// DSS-Java, the EU validator's own samples). If a future sample
// turns up with FlateDecode we'll add a decompress step here.
func extractStream(raw []byte, objNum int) ([]byte, error) {
	prefix := []byte(fmt.Sprintf("\n%d 0 obj", objNum))
	idx := bytes.Index(raw, prefix)
	if idx < 0 {
		// Try without leading newline (object at file start).
		prefix = []byte(fmt.Sprintf("%d 0 obj", objNum))
		idx = bytes.Index(raw, prefix)
		if idx < 0 {
			return nil, nil
		}
	}
	body := raw[idx:]
	streamMarker := []byte("\nstream\n")
	streamIdx := bytes.Index(body, streamMarker)
	if streamIdx < 0 {
		// Some PDFs use \r\n line endings.
		streamMarker = []byte("\nstream\r\n")
		streamIdx = bytes.Index(body, streamMarker)
		if streamIdx < 0 {
			return nil, nil
		}
	}
	contentStart := streamIdx + len(streamMarker)

	// Prefer /Length when present — DER-encoded OCSP/CRL bytes can
	// contain a literal `\nendstream` substring, so a string scan
	// would truncate. /Length is the canonical PDF way to size a
	// stream and every well-formed embedder writes it.
	if length, ok := readLengthFromStreamDict(body[:streamIdx]); ok {
		end := contentStart + length
		if end <= len(body) {
			return body[contentStart:end], nil
		}
	}
	endStreamIdx := bytes.Index(body[contentStart:], []byte("\nendstream"))
	if endStreamIdx < 0 {
		endStreamIdx = bytes.Index(body[contentStart:], []byte("\r\nendstream"))
		if endStreamIdx < 0 {
			return nil, nil
		}
	}
	return body[contentStart : contentStart+endStreamIdx], nil
}

// readLengthFromStreamDict pulls `/Length N` (literal int, not an
// indirect reference) out of the dict portion that precedes the
// `stream\n` marker. Returns false on indirect-reference-only
// dicts; the caller falls back to the `\nendstream` scan.
func readLengthFromStreamDict(dict []byte) (int, bool) {
	m := streamLengthRE.FindSubmatch(dict)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(string(m[1]))
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// streamLengthRE matches `/Length N` where N is a literal integer
// and not an indirect reference (`/Length N M R`).
var streamLengthRE = regexp.MustCompile(`/Length\s+(\d+)(?:[^0-9].|\s|>)`)

// fullCoverage reports whether the ByteRange covers everything
// except the Contents gap. If a doc has bytes after br[2]+br[3],
// those bytes WERE NOT signed — set TamperEvident=false on the
// report.
func fullCoverage(raw []byte, br [4]int64) bool {
	covered := br[0] + br[1] + br[3]
	// br[2] starts where Contents ends; the signed tail goes up to
	// br[2]+br[3]. The total length should match raw.
	end := br[2] + br[3]
	if end != int64(len(raw)) {
		return false
	}
	_ = covered
	return true
}
