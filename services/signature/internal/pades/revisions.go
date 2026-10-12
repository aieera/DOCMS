package pades

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Multi-revision tamper evidence (S-12).
//
// A signature's ByteRange covers the file as it was when that signature
// was applied. Every later signer, document timestamp or LTV embed appends
// an incremental update, so in any multi-signature PDF the earlier
// signatures are followed by bytes they do not cover. Those bytes are
// acceptable only when they are incremental updates that add signatures,
// timestamps or validation material, and nothing that changes what the
// earlier signer saw. laterRevisionsAllowed decides that, conservatively:
// anything it cannot positively classify is reported as tampering.
//
// An appended region passes when all of these hold:
//
//   - it ends with %%EOF (whitespace after it is tolerated);
//   - it has at least one classic xref table, every xref table in it
//     parses, every startxref in it points at one, and it contains no
//     cross-reference stream (those cannot be checked without decoding);
//   - every in-use xref entry points at a header of that object inside
//     the region, and no entry frees an object that existed before;
//   - every /Root in it names the catalog object in use before;
//   - every object it defines is either new, or a redefinition of one of
//     the objects below, identified by where the previous revision
//     references it — never by the keys it happens to carry:
//     the catalog named by /Root (only /AcroForm, /DSS, /Extensions,
//     /Version and /Metadata may change), the AcroForm the catalog names
//     (only /Fields and /SigFlags), a page (only /Annots), a signature
//     field not yet signed (a signed one may only be re-emitted
//     unchanged), the /DSS dict, its /VRI dicts, its /Certs /OCSPs /CRLs
//     streams, the catalog's XMP /Metadata stream, or the trailer /Info;
//   - no other stream is redefined unless its body is byte-identical (a
//     dict comparison alone never covers a stream: that is where page
//     content, images and form XObjects live);
//   - /Annots and /Fields only grow, and every added entry is a
//     signature field or a widget of one.
//
// New objects are not inspected: they can only change the document if an
// existing object is changed to reference them, and those changes are
// exactly what the rules above restrict.

// gapIsContents reports whether the signed ranges start at byte 0 and the
// unsigned gap between them is exactly this signature's /Contents hex
// string. Anything else leaves other bytes of the file unsigned.
func gapIsContents(raw []byte, s signatureBlock) bool {
	if s.ByteRange[0] != 0 || s.ContentsEnd <= s.ContentsStart || !byteRangeSane(s.ByteRange, int64(len(raw))) {
		return false
	}
	return s.ByteRange[0]+s.ByteRange[1] == int64(s.ContentsStart) && s.ByteRange[2] == int64(s.ContentsEnd)
}

// Bounds for the revision index on hostile input.
const (
	maxIndexedObjects = 1 << 20
	maxXrefEntries    = 1 << 20
	maxValueDepth     = 32
)

// The whole-file scans below use bytes.Index on a literal and then check
// the surroundings by hand: regexps without a literal prefix run at a few
// tens of MB/s, too slow for a 64 MiB upload under the 10 s budget.
var (
	objHeadRE   = regexp.MustCompile(`^(\d+)\s+(\d+)\s+obj\b`)
	startxrefRE = regexp.MustCompile(`startxref\s+(\d+)`)
	rootRefRE2  = regexp.MustCompile(`/Root\s+(\d+)\s+(\d+)\s+R`)
	infoRefRE   = regexp.MustCompile(`/Info\s+(\d+)\s+(\d+)\s+R`)
)

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isLetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

// scanObjHeaders finds every "N G obj" header anywhere in raw (not only
// at line starts: a reader follows xref offsets, not lines). It stops
// after max+1 hits.
func scanObjHeaders(raw []byte, max int) []objDef {
	var out []objDef
	for i := 0; i < len(raw); {
		k := bytes.Index(raw[i:], []byte("obj"))
		if k < 0 {
			break
		}
		p := i + k
		i = p + 3
		if p+3 < len(raw) && (isLetter(raw[p+3]) || isDigit(raw[p+3]) || raw[p+3] == '_') {
			continue
		}
		j := p
		if j == 0 || !isPDFWhite(raw[j-1]) {
			continue
		}
		for j > 0 && isPDFWhite(raw[j-1]) {
			j--
		}
		genEnd := j
		for j > 0 && isDigit(raw[j-1]) && genEnd-j < 10 {
			j--
		}
		if j == genEnd || j == 0 || !isPDFWhite(raw[j-1]) {
			continue
		}
		for j > 0 && isPDFWhite(raw[j-1]) {
			j--
		}
		numEnd := j
		for j > 0 && isDigit(raw[j-1]) && numEnd-j < 10 {
			j--
		}
		if j == numEnd || (j > 0 && isDigit(raw[j-1])) {
			continue
		}
		n, err := strconv.Atoi(string(raw[j:numEnd]))
		if err != nil {
			continue
		}
		out = append(out, objDef{num: n, off: j})
		if len(out) > max {
			break
		}
	}
	return out
}

// scanKeyword returns the offsets of a keyword that is not preceded by a
// letter and is followed by whitespace (e.g. "xref", not "startxref").
func scanKeyword(raw []byte, kw string) []int {
	var out []int
	for i := 0; i < len(raw); {
		k := bytes.Index(raw[i:], []byte(kw))
		if k < 0 {
			break
		}
		p := i + k
		i = p + len(kw)
		if p > 0 && isLetter(raw[p-1]) {
			continue
		}
		if i < len(raw) && !isPDFWhite(raw[i]) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// scanXRefNames returns the offsets of every /XRef or /XRefStm name: the
// marks of a cross-reference stream.
func scanXRefNames(raw []byte) []int {
	var out []int
	for i := 0; i < len(raw); {
		k := bytes.Index(raw[i:], []byte("/XRef"))
		if k < 0 {
			break
		}
		p := i + k
		i = p + len("/XRef")
		if i == len(raw) || isPDFWhite(raw[i]) || isPDFDelim(raw[i]) || bytes.HasPrefix(raw[i:], []byte("Stm")) {
			out = append(out, p)
		}
	}
	return out
}

type objDef struct{ num, off int }

type xrefEntry struct {
	num  int
	off  int64
	free bool
	at   int // offset of the section's "xref" keyword
}

type posRef struct{ at, val int }

// revIndex is built once per document and shared by every signature.
type revIndex struct {
	tooBig    bool
	defs      []objDef      // every "N G obj" header, by offset
	byNum     map[int][]int // object number → header offsets, ascending
	sections  []int         // offsets of parsed xref tables
	isSection map[int]bool
	badXref   []int // offsets of xref tables that did not parse
	entries   []xrefEntry
	startxref []posRef // at → target offset
	roots     []posRef // at → catalog object number
	infos     []posRef // at → document-info object number
	xrefStrm  []int
	dicts     map[int]*pdfValue // header offset → parsed dict (nil: not a dict)
}

func (d *parsedDoc) revisions() *revIndex {
	if d.rev == nil {
		d.rev = buildRevIndex(d.Bytes)
	}
	return d.rev
}

func buildRevIndex(raw []byte) *revIndex {
	ix := &revIndex{byNum: map[int][]int{}, isSection: map[int]bool{}, dicts: map[int]*pdfValue{}}
	ix.defs = scanObjHeaders(raw, maxIndexedObjects)
	if len(ix.defs) > maxIndexedObjects {
		ix.tooBig = true
		return ix
	}
	for _, d := range ix.defs {
		ix.byNum[d.num] = append(ix.byNum[d.num], d.off)
	}
	for _, at := range scanKeyword(raw, "xref") {
		entries, ok := parseXrefSection(raw, at, maxXrefEntries-len(ix.entries))
		if !ok {
			ix.badXref = append(ix.badXref, at)
			continue
		}
		ix.sections = append(ix.sections, at)
		ix.isSection[at] = true
		ix.entries = append(ix.entries, entries...)
	}
	for _, m := range startxrefRE.FindAllSubmatchIndex(raw, -1) {
		v, err := strconv.ParseInt(string(raw[m[2]:m[3]]), 10, 64)
		if err != nil || v > int64(len(raw)) {
			v = -1
		}
		ix.startxref = append(ix.startxref, posRef{at: m[0], val: int(v)})
	}
	for _, m := range rootRefRE2.FindAllSubmatchIndex(raw, -1) {
		n, err := strconv.Atoi(string(raw[m[2]:m[3]]))
		if err != nil {
			n = -1
		}
		ix.roots = append(ix.roots, posRef{at: m[0], val: n})
	}
	for _, m := range infoRefRE.FindAllSubmatchIndex(raw, -1) {
		if n, err := strconv.Atoi(string(raw[m[2]:m[3]])); err == nil {
			ix.infos = append(ix.infos, posRef{at: m[0], val: n})
		}
	}
	ix.xrefStrm = scanXRefNames(raw)
	return ix
}

// parseXrefSection parses a classic cross-reference table starting at the
// "xref" keyword at offset at, up to its "trailer" keyword.
func parseXrefSection(raw []byte, at, budget int) ([]xrefEntry, bool) {
	i := at + len("xref")
	var out []xrefEntry
	skip := func() {
		for i < len(raw) && isPDFWhite(raw[i]) {
			i++
		}
	}
	digits := func(exact int) (int64, bool) {
		j := i
		for j < len(raw) && raw[j] >= '0' && raw[j] <= '9' && (exact == 0 || j-i < exact) {
			j++
		}
		if j == i || (exact != 0 && j-i != exact) || j-i > 18 {
			return 0, false
		}
		v, err := strconv.ParseInt(string(raw[i:j]), 10, 64)
		i = j
		return v, err == nil
	}
	for {
		skip()
		if bytes.HasPrefix(raw[i:], []byte("trailer")) {
			return out, true
		}
		first, ok := digits(0)
		if !ok {
			return nil, false
		}
		skip()
		count, ok := digits(0)
		if !ok || count > int64(budget-len(out)) {
			return nil, false
		}
		for k := int64(0); k < count; k++ {
			skip()
			off, ok := digits(10)
			if !ok {
				return nil, false
			}
			skip()
			if _, ok := digits(5); !ok {
				return nil, false
			}
			skip()
			if i >= len(raw) || (raw[i] != 'n' && raw[i] != 'f') {
				return nil, false
			}
			out = append(out, xrefEntry{num: int(first + k), off: off, free: raw[i] == 'f', at: at})
			i++
		}
	}
}

// lastDefBefore returns the offset of the last header of object num that
// starts before limit.
func (ix *revIndex) lastDefBefore(num, limit int) (int, bool) {
	offs := ix.byNum[num]
	k := sort.SearchInts(offs, limit)
	if k == 0 {
		return 0, false
	}
	return offs[k-1], true
}

// latestDict returns the object's last definition in the file as a dict.
func (ix *revIndex) latestDict(raw []byte, num int) *pdfValue {
	off, ok := ix.lastDefBefore(num, len(raw)+1)
	if !ok {
		return nil
	}
	return ix.dictAt(raw, off)
}

// headerAt reports whether an "num G obj" header starts at off.
func headerAt(raw []byte, off int64, num int) bool {
	if off < 0 || off >= int64(len(raw)) {
		return false
	}
	end := off + 48
	if end > int64(len(raw)) {
		end = int64(len(raw))
	}
	m := objHeadRE.FindSubmatch(raw[off:end])
	if m == nil {
		return false
	}
	n, err := strconv.Atoi(string(m[1]))
	return err == nil && n == num
}

// dictAt parses the top-level value of the object whose header starts at
// off; nil when it is not a dictionary or does not parse.
func (ix *revIndex) dictAt(raw []byte, off int) *pdfValue {
	if v, ok := ix.dicts[off]; ok {
		return v
	}
	var out *pdfValue
	end := off + 48
	if end > len(raw) {
		end = len(raw)
	}
	if m := objHeadRE.FindIndex(raw[off:end]); m != nil {
		lx := newLexer(raw, off+m[1])
		if v, err := lx.value(); err == nil && v.kind == 'd' {
			readStreamBody(raw, lx.i, &v)
			out = &v
		}
	}
	ix.dicts[off] = out
	return out
}

// readStreamBody marks v as a stream when the `stream` keyword follows its
// dict at i, and records the body: /Length bytes when /Length is a direct
// integer that lands on `endstream`, else up to the next `endstream`.
// bodyOK=false when no body boundary could be found.
func readStreamBody(raw []byte, i int, v *pdfValue) {
	for i < len(raw) && isPDFWhite(raw[i]) {
		i++
	}
	if !bytes.HasPrefix(raw[i:], []byte("stream")) {
		return
	}
	v.stream = true
	p := i + len("stream")
	if bytes.HasPrefix(raw[p:], []byte("\r\n")) {
		p += 2
	} else if p < len(raw) && (raw[p] == '\n' || raw[p] == '\r') {
		p++
	}
	if l, ok := v.dict["/Length"]; ok && l.kind == 'N' {
		if n, err := strconv.Atoi(l.canon); err == nil && n >= 0 && n <= len(raw)-p {
			q := p + n
			for q < len(raw) && isPDFWhite(raw[q]) {
				q++
			}
			if bytes.HasPrefix(raw[q:], []byte("endstream")) {
				v.body, v.bodyOK = raw[p:p+n], true
				return
			}
		}
	}
	if k := bytes.Index(raw[p:], []byte("endstream")); k >= 0 {
		body := raw[p : p+k]
		body = bytes.TrimSuffix(body, []byte("\n"))
		body = bytes.TrimSuffix(body, []byte("\r"))
		v.body, v.bodyOK = body, true
	}
}

// revCtx names the objects the revision before `from` references from
// the places where a later update may legitimately change them.
//
// That revision may be attacker-authored (the shadow-attack model: the
// attacker prepares the document the victim signs), so nothing recorded
// here may by itself permit rewriting a stream or an arbitrary dict. The
// rules that use it also check the object's own shape (see
// redefinitionAllowed).
type revCtx struct {
	root     int
	acroForm int
	dssDict  map[int]bool
	vriMap   map[int]bool // the /DSS /VRI dict (keyed by signature hash)
	vriEntry map[int]bool // its per-signature entries
	dssData  map[int]bool // /Certs /OCSPs /CRLs (and VRI /Cert /OCSP /CRL /TS); never rewritable
	info     map[int]bool
}

func newRevCtx(root int) *revCtx {
	return &revCtx{root: root, acroForm: -1, dssDict: map[int]bool{}, vriMap: map[int]bool{},
		vriEntry: map[int]bool{}, dssData: map[int]bool{}, info: map[int]bool{}}
}

// isDSSMaterial reports whether the revision before the update already
// referenced r from its /DSS.
func (rc *revCtx) isDSSMaterial(r int) bool {
	return rc.dssDict[r] || rc.vriMap[r] || rc.vriEntry[r] || rc.dssData[r]
}

func (ix *revIndex) buildRevCtx(raw []byte, root, from int) *revCtx {
	rc := newRevCtx(root)
	for _, r := range ix.infos {
		if r.at < from {
			rc.info[r.val] = true
		}
	}
	prior := ix.resolveDict(raw, pdfValue{kind: 'r', ref: root}, from)
	if prior != nil {
		if v := prior.dict["/AcroForm"]; v.kind == 'r' {
			rc.acroForm = v.ref
		}
		// Only the catalog in force before the update says what is DSS
		// material. The latest catalog is the update's own: letting it
		// widen the set would let the update name any signed object as a
		// certificate and then rewrite it. DSS material an update adds is
		// made of new objects and needs no redefinition allowance.
		ix.collectDSS(raw, prior, from, rc)
	}
	return rc
}

// collectDSS records the /DSS dict a catalog names and the validation
// material it references.
func (ix *revIndex) collectDSS(raw []byte, cat *pdfValue, limit int, rc *revCtx) {
	v := cat.dict["/DSS"]
	if v.kind == 'r' {
		rc.dssDict[v.ref] = true
	}
	dss := ix.resolveDict(raw, v, limit)
	if dss == nil {
		return
	}
	addRefs := func(d *pdfValue, keys ...string) {
		for _, k := range keys {
			e := d.dict[k]
			if e.kind == 'r' {
				rc.dssData[e.ref] = true
			}
			if refs, ok := refsOf(e); ok {
				for _, r := range refs {
					rc.dssData[r] = true
				}
			}
		}
	}
	addRefs(dss, "/Certs", "/OCSPs", "/CRLs")
	vv := dss.dict["/VRI"]
	if vv.kind == 'r' {
		rc.vriMap[vv.ref] = true
	}
	vri := ix.resolveDict(raw, vv, limit)
	if vri == nil {
		return
	}
	for _, e := range vri.dict {
		if e.kind == 'r' {
			rc.vriEntry[e.ref] = true
		}
		if d := ix.resolveDict(raw, e, limit); d != nil {
			addRefs(d, "/Cert", "/OCSP", "/CRL", "/TS")
		}
	}
}

// dssRefsAllowed checks every /DSS the update puts in force (through a
// catalog it writes, or by redefining the DSS dictionaries): each object
// it reaches must be new in the update, or already be DSS material of the
// catalog in force before it (DSS reuses known certificate, OCSP and CRL
// objects when it writes a new /DSS). Pointing a /DSS at any other signed
// object is refused, whether or not that object is rewritten.
func (ix *revIndex) dssRefsAllowed(raw []byte, rc *revCtx, root, from int) error {
	var cats []*pdfValue
	for _, off := range ix.byNum[root] {
		if off >= from {
			if d := ix.dictAt(raw, off); d != nil {
				cats = append(cats, d)
			}
		}
	}
	if d := ix.latestDict(raw, root); d != nil {
		cats = append(cats, d)
	}
	for _, cat := range cats {
		reach := newRevCtx(root)
		ix.collectDSS(raw, cat, len(raw)+1, reach)
		var bad []int
		for _, set := range []map[int]bool{reach.dssDict, reach.vriMap, reach.vriEntry, reach.dssData} {
			for r := range set {
				if rc.isDSSMaterial(r) {
					continue
				}
				if _, existed := ix.lastDefBefore(r, from); existed {
					bad = append(bad, r)
				}
			}
		}
		if len(bad) > 0 {
			sort.Ints(bad)
			return fmt.Errorf("later /DSS references signed object %d", bad[0])
		}
	}
	return nil
}

// laterRevisionsAllowed reports nil when raw[from:] consists only of
// incremental updates that add signatures, timestamps or validation
// material; otherwise an error naming the first offending change.
func laterRevisionsAllowed(ctx context.Context, doc *parsedDoc, from int) error {
	raw := doc.Bytes
	if from >= len(raw) {
		return nil
	}
	ix := doc.revisions()
	if ix.tooBig {
		return errors.New("too many objects to check the later revisions")
	}
	trimmed := bytes.TrimRight(raw, "\x00\t\n\f\r ")
	if !bytes.HasSuffix(trimmed, []byte("%%EOF")) || len(trimmed)-len("%%EOF") < from {
		return errors.New("appended bytes are not a complete incremental update")
	}
	for _, p := range ix.xrefStrm {
		if p >= from {
			return errors.New("later revision uses a cross-reference stream")
		}
	}
	for _, p := range ix.badXref {
		if p >= from {
			return errors.New("later revision has a malformed xref table")
		}
	}
	sections := 0
	for _, p := range ix.sections {
		if p >= from {
			sections++
		}
	}
	if sections == 0 {
		return errors.New("appended bytes contain no xref table")
	}
	for _, s := range ix.startxref {
		if s.at >= from && !ix.isSection[s.val] {
			return errors.New("later startxref does not point at an xref table")
		}
	}

	root := -1
	for _, r := range ix.roots {
		if r.at < from {
			root = r.val
		}
	}
	if root < 0 {
		return errors.New("no catalog reference before the later revisions")
	}
	for _, r := range ix.roots {
		if r.at >= from && r.val != root {
			return fmt.Errorf("later revision replaces the catalog (object %d → %d)", root, r.val)
		}
	}

	type def struct{ num, off int }
	var tail []def
	for _, e := range ix.entries {
		if e.at < from || e.num == 0 {
			continue
		}
		if e.free {
			if _, existed := ix.lastDefBefore(e.num, from); existed {
				return fmt.Errorf("later revision deletes object %d", e.num)
			}
			continue
		}
		if e.off < int64(from) || !headerAt(raw, e.off, e.num) {
			return fmt.Errorf("later xref entry for object %d does not point at it inside the update", e.num)
		}
		tail = append(tail, def{e.num, int(e.off)})
	}
	for k := sort.Search(len(ix.defs), func(i int) bool { return ix.defs[i].off >= from }); k < len(ix.defs); k++ {
		tail = append(tail, def{ix.defs[k].num, ix.defs[k].off})
	}
	rc := ix.buildRevCtx(raw, root, from)
	if err := ix.dssRefsAllowed(raw, rc, root, from); err != nil {
		return err
	}
	seen := map[int]bool{}
	for _, d := range tail {
		if seen[d.off] {
			continue
		}
		seen[d.off] = true
		if err := ctx.Err(); err != nil {
			return err
		}
		prevOff, existed := ix.lastDefBefore(d.num, from)
		if !existed {
			continue // a new object
		}
		if err := ix.redefinitionAllowed(raw, rc, d.num, prevOff, d.off, from); err != nil {
			return err
		}
	}
	return nil
}

// redefinitionAllowed checks one object that a later revision redefines.
// Which rule applies is decided by where the previous revision references
// the object (rc), not by the keys the object carries.
func (ix *revIndex) redefinitionAllowed(raw []byte, rc *revCtx, num, prevOff, curOff, from int) error {
	prev, cur := ix.dictAt(raw, prevOff), ix.dictAt(raw, curOff)
	if prev == nil || cur == nil {
		return fmt.Errorf("later revision rewrites object %d", num)
	}
	pt, ct := prev.nameOf("/Type"), cur.nameOf("/Type")
	if prev.stream || cur.stream {
		// No allowance covers a stream, whatever references it: DSS
		// material is append-only and XMP updates are new objects (the
		// catalog's /Metadata may be re-pointed). A stream may only be
		// re-emitted unchanged: the same body and the same dict, /Length
		// aside (a /Filter or /DecodeParms change re-reads the same bytes).
		if prev.stream && cur.stream && prev.bodyOK && cur.bodyOK &&
			bytes.Equal(prev.body, cur.body) && equalExcept(prev, cur, "/Length") {
			return nil
		}
		return fmt.Errorf("later revision rewrites stream object %d", num)
	}
	switch {
	case num == rc.root:
		if pt != "/Catalog" || ct != "/Catalog" {
			return errors.New("later revision rewrites the catalog")
		}
		if !equalExcept(prev, cur, "/AcroForm", "/DSS", "/Extensions", "/Version", "/Metadata") {
			return errors.New("later revision changes the catalog beyond /AcroForm and /DSS")
		}
		return ix.acroFormValueAllowed(raw, prev.dict["/AcroForm"], cur.dict["/AcroForm"], from)
	case num == rc.acroForm:
		return ix.acroFormAllowed(raw, prev, cur)
	case isSigField(prev):
		if !isSigField(cur) {
			return fmt.Errorf("later revision turns signature field %d into something else", num)
		}
		if _, signed := prev.dict["/V"]; signed && !equalExcept(prev, cur) {
			// A signed field may only be re-emitted unchanged: a new
			// /AP, /Rect or /F would paint over the signed page.
			return fmt.Errorf("later revision changes signed field %d", num)
		}
		return nil
	case pt == "/Page" && ct == "/Page":
		if !equalExcept(prev, cur, "/Annots") {
			return fmt.Errorf("later revision changes page object %d beyond /Annots", num)
		}
		return ix.listGrowsWithSigFields(raw, prev.dict["/Annots"], cur.dict["/Annots"], "/Annots")
	case rc.dssDict[num]:
		if err := ix.appendOnly(prev, cur, isDSSKey, dssArrayKeys, "/DSS", from); err != nil {
			return fmt.Errorf("later revision changes /DSS object %d: %w", num, err)
		}
		return nil
	case rc.vriMap[num]:
		if err := ix.appendOnly(prev, cur, isVRIMapKey, nil, "/VRI", from); err != nil {
			return fmt.Errorf("later revision changes /VRI object %d: %w", num, err)
		}
		return nil
	case rc.vriEntry[num]:
		if err := ix.appendOnly(prev, cur, isVRIEntryKey, vriArrayKeys, "/VRI", from); err != nil {
			return fmt.Errorf("later revision changes VRI entry %d: %w", num, err)
		}
		return nil
	case rc.info[num] && isInfo(prev) && isInfo(cur):
		return nil
	}
	return fmt.Errorf("later revision rewrites object %d", num)
}

var (
	dssArrayKeys = map[string]bool{"/Certs": true, "/OCSPs": true, "/CRLs": true}
	vriArrayKeys = map[string]bool{"/Cert": true, "/OCSP": true, "/CRL": true}
)

func isDSSKey(k string) bool      { return k == "/Type" || k == "/VRI" || dssArrayKeys[k] }
func isVRIMapKey(k string) bool   { return k == "/Type" || vriKeyRE.MatchString(k) }
func isVRIEntryKey(k string) bool { return k == "/Type" || k == "/TS" || k == "/TU" || vriArrayKeys[k] }

// appendOnly allows a redefined DSS-shaped dict (the /DSS, its /VRI map,
// or a VRI entry) to change only by growing. Both definitions must carry
// only keys keyOK accepts and a /Type of typ (or none). Nothing may be
// removed or changed; an array in arrays may gain references, and a new
// key may be added, only when every object it references is new in the
// update. Because the previous definition's own shape is checked, a
// catalog that names some other dict as its /DSS gains nothing.
func (ix *revIndex) appendOnly(prev, cur *pdfValue, keyOK func(string) bool, arrays map[string]bool, typ string, from int) error {
	for _, d := range []*pdfValue{prev, cur} {
		for k := range d.dict {
			if !keyOK(k) {
				return fmt.Errorf("key %s is not allowed", k)
			}
		}
		if t := d.nameOf("/Type"); t != "" && t != typ {
			return fmt.Errorf("/Type %s, want %s", t, typ)
		}
	}
	for k := range prev.dict {
		if _, ok := cur.dict[k]; !ok {
			return fmt.Errorf("removes %s", k)
		}
	}
	for k, cv := range cur.dict {
		pv, had := prev.dict[k]
		switch {
		case k == "/Type" || (had && pv.canon == cv.canon):
			continue
		case arrays[k] && cv.kind == 'a' && (!had || pv.kind == 'a'):
			pr, ok1 := refsOf(pv)
			cr, ok2 := refsOf(cv)
			if !ok1 || !ok2 {
				return fmt.Errorf("%s is not an array of references", k)
			}
			kept := map[int]bool{}
			for _, r := range cr {
				kept[r] = true
			}
			for _, r := range pr {
				if !kept[r] {
					return fmt.Errorf("%s drops object %d", k, r)
				}
				delete(kept, r)
			}
			for r := range kept {
				if !ix.isNew(r, from) {
					return fmt.Errorf("%s gains signed object %d", k, r)
				}
			}
		case !had && ix.refsAllNew(cv, from):
			continue
		default:
			return fmt.Errorf("changes %s", k)
		}
	}
	return nil
}

// isNew reports whether object r has no definition before from.
func (ix *revIndex) isNew(r, from int) bool {
	_, existed := ix.lastDefBefore(r, from)
	return !existed
}

// refsAllNew reports whether every reference inside v names an object
// that is new in the update.
func (ix *revIndex) refsAllNew(v pdfValue, from int) bool {
	switch v.kind {
	case 'r':
		return ix.isNew(v.ref, from)
	case 'a':
		for _, it := range v.items {
			if !ix.refsAllNew(it, from) {
				return false
			}
		}
	case 'd':
		for _, it := range v.dict {
			if !ix.refsAllNew(it, from) {
				return false
			}
		}
	}
	return true
}

func (ix *revIndex) acroFormValueAllowed(raw []byte, pv, cv pdfValue, from int) error {
	if pv.kind == 0 && cv.kind == 0 {
		return nil
	}
	if pv.kind != 0 && pv.canon == cv.canon {
		return nil // unchanged (a referenced AcroForm object is checked on its own)
	}
	if cv.kind == 0 {
		return errors.New("later revision removes the AcroForm")
	}
	prev := &pdfValue{kind: 'd', dict: map[string]pdfValue{}}
	if pv.kind != 0 {
		prev = ix.resolveDict(raw, pv, from)
	}
	cur := ix.resolveDict(raw, cv, len(raw)+1)
	if prev == nil || cur == nil {
		return errors.New("later revision has an unreadable AcroForm")
	}
	return ix.acroFormAllowed(raw, prev, cur)
}

func (ix *revIndex) acroFormAllowed(raw []byte, prev, cur *pdfValue) error {
	if !equalExcept(prev, cur, "/Fields", "/SigFlags") {
		return errors.New("later revision changes the AcroForm beyond /Fields and /SigFlags")
	}
	return ix.listGrowsWithSigFields(raw, prev.dict["/Fields"], cur.dict["/Fields"], "/Fields")
}

// resolveDict returns an inline dict, or the last definition before limit
// of a referenced one.
func (ix *revIndex) resolveDict(raw []byte, v pdfValue, limit int) *pdfValue {
	switch v.kind {
	case 'd':
		return &v
	case 'r':
		off, ok := ix.lastDefBefore(v.ref, limit)
		if !ok {
			return nil
		}
		return ix.dictAt(raw, off)
	}
	return nil
}

// listGrowsWithSigFields checks an /Annots or /Fields array: nothing
// removed, and everything added is a signature field or its widget.
func (ix *revIndex) listGrowsWithSigFields(raw []byte, pv, cv pdfValue, key string) error {
	if pv.kind != 0 && pv.canon == cv.canon {
		return nil
	}
	prev, ok := refsOf(pv)
	if !ok {
		return fmt.Errorf("later revision changes an indirect %s array", key)
	}
	cur, ok := refsOf(cv)
	if !ok {
		return fmt.Errorf("later revision writes an unreadable %s array", key)
	}
	have := map[int]bool{}
	for _, r := range cur {
		have[r] = true
	}
	was := map[int]bool{}
	for _, r := range prev {
		if !have[r] {
			return fmt.Errorf("later revision removes object %d from %s", r, key)
		}
		was[r] = true
	}
	for _, r := range cur {
		if was[r] {
			continue
		}
		d := ix.latestDict(raw, r)
		if d == nil {
			return fmt.Errorf("later revision adds unreadable object %d to %s", r, key)
		}
		if isSigField(d) {
			continue
		}
		if d.nameOf("/Subtype") == "/Widget" {
			if p, ok := d.dict["/Parent"]; ok && p.kind == 'r' {
				if parent := ix.latestDict(raw, p.ref); parent != nil && isSigField(parent) {
					continue
				}
			}
		}
		return fmt.Errorf("later revision adds non-signature object %d to %s", r, key)
	}
	return nil
}

// refsOf returns the object numbers of an absent value or an array of
// references; ok=false for anything else.
func refsOf(v pdfValue) ([]int, bool) {
	switch v.kind {
	case 0:
		return nil, true
	case 'a':
		out := make([]int, 0, len(v.items))
		for _, it := range v.items {
			if it.kind != 'r' {
				return nil, false
			}
			out = append(out, it.ref)
		}
		return out, true
	}
	return nil, false
}

func isSigField(d *pdfValue) bool { return d != nil && d.nameOf("/FT") == "/Sig" }

var vriKeyRE = regexp.MustCompile(`^/[0-9A-Fa-f]{40}$`)

var infoKeys = map[string]bool{
	"/Title": true, "/Author": true, "/Subject": true, "/Keywords": true, "/Creator": true,
	"/Producer": true, "/CreationDate": true, "/ModDate": true, "/Trapped": true,
}

func isInfo(d *pdfValue) bool {
	if len(d.dict) == 0 {
		return false
	}
	for k := range d.dict {
		if !infoKeys[k] {
			return false
		}
	}
	return true
}

// equalExcept compares two dicts key by key, ignoring the listed keys.
func equalExcept(a, b *pdfValue, ignore ...string) bool {
	skip := map[string]bool{}
	for _, k := range ignore {
		skip[k] = true
	}
	for k, v := range a.dict {
		if !skip[k] && b.dict[k].canon != v.canon {
			return false
		}
	}
	for k := range b.dict {
		if _, ok := a.dict[k]; !ok && !skip[k] {
			return false
		}
	}
	return true
}

// ----- a minimal PDF object reader ---------------------------------

// pdfValue is a parsed PDF object with a canonical text form that is
// independent of whitespace, number spelling, name escapes and string
// encoding, so two serializations of the same object compare equal.
type pdfValue struct {
	kind  byte // 'n' name, 'N' number, 's' string, 'k' keyword, 'r' reference, 'a' array, 'd' dict; 0 = absent
	canon string
	ref   int
	items []pdfValue
	dict  map[string]pdfValue
	// Set by dictAt for a top-level object followed by `stream`.
	stream bool
	body   []byte
	bodyOK bool
}

func (v *pdfValue) has(key string) bool {
	_, ok := v.dict[key]
	return ok
}

func (v *pdfValue) nameOf(key string) string {
	if e, ok := v.dict[key]; ok && e.kind == 'n' {
		return e.canon
	}
	return ""
}

var errValue = errors.New("pades: malformed PDF value")

type pdfLexer struct {
	b     []byte
	i     int
	limit int
	depth int
}

func newLexer(b []byte, at int) *pdfLexer {
	limit := len(b)
	if at+maxDictWalk < limit {
		limit = at + maxDictWalk
	}
	return &pdfLexer{b: b, i: at, limit: limit}
}

func isPDFWhite(c byte) bool {
	return c == 0 || c == '\t' || c == '\n' || c == '\f' || c == '\r' || c == ' '
}

func isPDFDelim(c byte) bool { return strings.IndexByte("()<>[]{}/%", c) >= 0 }

func (l *pdfLexer) skipWS() {
	for l.i < l.limit {
		c := l.b[l.i]
		switch {
		case isPDFWhite(c):
			l.i++
		case c == '%':
			for l.i < l.limit && l.b[l.i] != '\n' && l.b[l.i] != '\r' {
				l.i++
			}
		default:
			return
		}
	}
}

func (l *pdfLexer) regular() string {
	j := l.i
	for j < l.limit && !isPDFWhite(l.b[j]) && !isPDFDelim(l.b[j]) {
		j++
	}
	s := string(l.b[l.i:j])
	l.i = j
	return s
}

func (l *pdfLexer) value() (pdfValue, error) {
	l.skipWS()
	if l.i >= l.limit {
		return pdfValue{}, errValue
	}
	c := l.b[l.i]
	switch {
	case c == '/':
		l.i++
		return pdfValue{kind: 'n', canon: "/" + decodeName(l.regular())}, nil
	case c == '(':
		s, err := l.literal()
		return pdfValue{kind: 's', canon: "s:" + hex.EncodeToString(s)}, err
	case c == '<' && l.i+1 < l.limit && l.b[l.i+1] == '<':
		return l.dictValue()
	case c == '<':
		s, err := l.hexString()
		return pdfValue{kind: 's', canon: "s:" + hex.EncodeToString(s)}, err
	case c == '[':
		return l.arrayValue()
	case c == '+' || c == '-' || c == '.' || (c >= '0' && c <= '9'):
		return l.numberOrRef()
	case !isPDFDelim(c):
		kw := l.regular()
		if kw == "true" || kw == "false" || kw == "null" {
			return pdfValue{kind: 'k', canon: kw}, nil
		}
	}
	return pdfValue{}, errValue
}

func (l *pdfLexer) enter() error {
	l.depth++
	if l.depth > maxValueDepth {
		return errValue
	}
	return nil
}

func (l *pdfLexer) dictValue() (pdfValue, error) {
	if err := l.enter(); err != nil {
		return pdfValue{}, err
	}
	defer func() { l.depth-- }()
	l.i += 2
	d := map[string]pdfValue{}
	for {
		l.skipWS()
		if l.i+1 < l.limit && l.b[l.i] == '>' && l.b[l.i+1] == '>' {
			l.i += 2
			break
		}
		if l.i >= l.limit || l.b[l.i] != '/' {
			return pdfValue{}, errValue
		}
		k, err := l.value()
		if err != nil {
			return pdfValue{}, err
		}
		v, err := l.value()
		if err != nil {
			return pdfValue{}, err
		}
		d[k.canon] = v
	}
	keys := make([]string, 0, len(d))
	for k := range d {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	sb.WriteString("<<")
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteByte(' ')
		sb.WriteString(d[k].canon)
		sb.WriteByte(' ')
	}
	sb.WriteString(">>")
	return pdfValue{kind: 'd', canon: sb.String(), dict: d}, nil
}

func (l *pdfLexer) arrayValue() (pdfValue, error) {
	if err := l.enter(); err != nil {
		return pdfValue{}, err
	}
	defer func() { l.depth-- }()
	l.i++
	var items []pdfValue
	parts := []string{}
	for {
		l.skipWS()
		if l.i < l.limit && l.b[l.i] == ']' {
			l.i++
			break
		}
		v, err := l.value()
		if err != nil {
			return pdfValue{}, err
		}
		items = append(items, v)
		parts = append(parts, v.canon)
	}
	return pdfValue{kind: 'a', canon: "[" + strings.Join(parts, " ") + "]", items: items}, nil
}

func (l *pdfLexer) numberOrRef() (pdfValue, error) {
	tok := l.regular()
	f, err := strconv.ParseFloat(tok, 64)
	if err != nil {
		return pdfValue{}, errValue
	}
	num := pdfValue{kind: 'N', canon: strconv.FormatFloat(f, 'g', -1, 64)}
	n, err := strconv.Atoi(tok)
	if err != nil || n < 0 || tok[0] == '+' {
		return num, nil
	}
	save := l.i
	l.skipWS()
	gen := l.regular()
	if g, err := strconv.Atoi(gen); err == nil && g >= 0 {
		l.skipWS()
		if l.i < l.limit && l.b[l.i] == 'R' && (l.i+1 >= l.limit || isPDFWhite(l.b[l.i+1]) || isPDFDelim(l.b[l.i+1])) {
			l.i++
			return pdfValue{kind: 'r', canon: fmt.Sprintf("%d %d R", n, g), ref: n}, nil
		}
	}
	l.i = save
	return num, nil
}

func (l *pdfLexer) literal() ([]byte, error) {
	l.i++ // (
	var out []byte
	depth := 1
	for l.i < l.limit {
		c := l.b[l.i]
		l.i++
		switch c {
		case '(':
			depth++
			out = append(out, c)
		case ')':
			depth--
			if depth == 0 {
				return out, nil
			}
			out = append(out, c)
		case '\r':
			if l.i < l.limit && l.b[l.i] == '\n' {
				l.i++
			}
			out = append(out, '\n')
		case '\\':
			if l.i >= l.limit {
				return nil, errValue
			}
			e := l.b[l.i]
			l.i++
			switch e {
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 't':
				out = append(out, '\t')
			case 'b':
				out = append(out, '\b')
			case 'f':
				out = append(out, '\f')
			case '\r':
				if l.i < l.limit && l.b[l.i] == '\n' {
					l.i++
				}
			case '\n':
			default:
				if e >= '0' && e <= '7' {
					v := int(e - '0')
					for k := 0; k < 2 && l.i < l.limit && l.b[l.i] >= '0' && l.b[l.i] <= '7'; k++ {
						v = v*8 + int(l.b[l.i]-'0')
						l.i++
					}
					out = append(out, byte(v))
				} else {
					out = append(out, e)
				}
			}
		default:
			out = append(out, c)
		}
	}
	return nil, errValue
}

func (l *pdfLexer) hexString() ([]byte, error) {
	l.i++ // <
	var digits []byte
	for l.i < l.limit {
		c := l.b[l.i]
		l.i++
		switch {
		case c == '>':
			if len(digits)%2 == 1 {
				digits = append(digits, '0')
			}
			out := make([]byte, len(digits)/2)
			if _, err := hex.Decode(out, digits); err != nil {
				return nil, errValue
			}
			return out, nil
		case isPDFWhite(c):
		case (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F'):
			digits = append(digits, c)
		default:
			return nil, errValue
		}
	}
	return nil, errValue
}

// decodeName resolves #xx escapes in a PDF name.
func decodeName(s string) string {
	if !strings.Contains(s, "#") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '#' && i+2 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
				b.WriteByte(byte(v))
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
