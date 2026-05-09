package pades

import (
	"bytes"
	"crypto/sha1" //nolint:gosec // SHA-1 is the PAdES /VRI key per spec
	"encoding/hex"
	"fmt"
	"regexp"
)

// dssEmbed appends a /DSS dictionary + /VRI entry to a signed PDF
// via incremental update. Output preserves the original bytes
// (the existing signature's ByteRange stays valid) and adds new
// objects + an updated catalog at the tail.
//
// What we DON'T do here:
//   - Add a /DocTimeStamp revision (B-T → B-LT promotion). The
//     embedder calls Sign() on the signer interface for that step;
//     this function is the LTV-material attach only.
//   - Rebuild the xref table accurately. We append a manual
//     `xref / trailer / startxref` chunk that lists ONLY the new
//     objects we wrote — Adobe's incremental-update convention.
//     Most readers accept this; the Tier-2 EU DSS validator is the
//     canonical conformance check.
//
// Inputs:
//   - original  the signed PDF bytes (one or more sigs already
//               present + their ByteRange already correct)
//   - mats      one entry per signature; cert chain + OCSP/CRL
//               material to attach.
//
// Returns the new full PDF bytes (concatenation of original + the
// incremental update).

// dssMaterial holds everything we want to embed for one signature.
type dssMaterial struct {
	// SignatureContents is the hex-decoded /Contents of the
	// signature this VRI describes. SHA-1 over it gives the /VRI
	// key per ETSI EN 319 142-1.
	SignatureContents []byte
	// CertChain is leaf-first cert DER. Goes into /Certs.
	CertChain [][]byte
	// OCSPResponses are raw DER OCSP responses. Goes into /OCSPs.
	OCSPResponses [][]byte
	// CRLs are raw DER CRLs. Goes into /CRLs.
	CRLs [][]byte
}

// vriKey returns the SHA-1 hex (uppercase) of contents — what the
// /VRI map keys on per ETSI.
func vriKey(contents []byte) string {
	h := sha1.Sum(contents) //nolint:gosec
	return hex.EncodeToString(h[:])
}

// embedDSS produces an incremental update + appends it.
func embedDSS(original []byte, mats []dssMaterial) ([]byte, error) {
	if len(mats) == 0 {
		return original, nil
	}

	// Find the highest-numbered object so our new objects don't
	// collide.
	maxObj := 0
	for _, m := range objHeaderRE.FindAllSubmatch(original, -1) {
		n := bytesToInt(m[1])
		if n > maxObj {
			maxObj = n
		}
	}

	var buf bytes.Buffer
	buf.Write(original)
	if !bytes.HasSuffix(original, []byte("\n")) {
		buf.WriteString("\n")
	}

	type objWrite struct {
		Num    int
		Offset int64
	}
	var objs []objWrite
	emit := func(obj int, body []byte) {
		off := int64(buf.Len())
		fmt.Fprintf(&buf, "%d 0 obj\n", obj)
		buf.Write(body)
		buf.WriteString("\nendobj\n")
		objs = append(objs, objWrite{Num: obj, Offset: off})
	}

	var allCertObjs, allOCSPObjs, allCRLObjs []int
	type vriEntryInts struct{ certs, ocsps, crls []int }
	vriEntries := map[string]vriEntryInts{}

	for _, m := range mats {
		var certNums, ocspNums, crlNums []int
		for _, c := range m.CertChain {
			maxObj++
			emit(maxObj, encodeStream(c))
			certNums = append(certNums, maxObj)
			allCertObjs = append(allCertObjs, maxObj)
		}
		for _, o := range m.OCSPResponses {
			maxObj++
			emit(maxObj, encodeStream(o))
			ocspNums = append(ocspNums, maxObj)
			allOCSPObjs = append(allOCSPObjs, maxObj)
		}
		for _, c := range m.CRLs {
			maxObj++
			emit(maxObj, encodeStream(c))
			crlNums = append(crlNums, maxObj)
			allCRLObjs = append(allCRLObjs, maxObj)
		}
		vriEntries[vriKey(m.SignatureContents)] = vriEntryInts{certNums, ocspNums, crlNums}
	}

	// /VRI dict object.
	maxObj++
	vriObj := maxObj
	{
		var w bytes.Buffer
		w.WriteString("<<\n")
		for k, v := range vriEntries {
			fmt.Fprintf(&w, "/%s <<\n", k)
			if len(v.certs) > 0 {
				fmt.Fprintf(&w, "  /Cert [%s]\n", refList(v.certs))
			}
			if len(v.ocsps) > 0 {
				fmt.Fprintf(&w, "  /OCSP [%s]\n", refList(v.ocsps))
			}
			if len(v.crls) > 0 {
				fmt.Fprintf(&w, "  /CRL [%s]\n", refList(v.crls))
			}
			w.WriteString(">>\n")
		}
		w.WriteString(">>")
		emit(vriObj, w.Bytes())
	}

	// /DSS dict object.
	maxObj++
	dssObj := maxObj
	{
		var w bytes.Buffer
		w.WriteString("<<\n/Type /DSS\n")
		if len(allCertObjs) > 0 {
			fmt.Fprintf(&w, "/Certs [%s]\n", refList(allCertObjs))
		}
		if len(allOCSPObjs) > 0 {
			fmt.Fprintf(&w, "/OCSPs [%s]\n", refList(allOCSPObjs))
		}
		if len(allCRLObjs) > 0 {
			fmt.Fprintf(&w, "/CRLs [%s]\n", refList(allCRLObjs))
		}
		fmt.Fprintf(&w, "/VRI %d 0 R\n>>", vriObj)
		emit(dssObj, w.Bytes())
	}

	// Re-emit the catalog with /DSS added. Find the original catalog
	// object number from /Root in the trailer + extract its body.
	rootObj, rootBody, ok := findOriginalCatalog(original)
	rootRef := dssObj
	if ok {
		body := bytes.TrimSpace(rootBody)
		if bytes.HasSuffix(body, []byte(">>")) {
			body = body[:len(body)-2]
		}
		var w bytes.Buffer
		w.Write(body)
		fmt.Fprintf(&w, "/DSS %d 0 R\n>>", dssObj)
		emit(rootObj, w.Bytes())
		rootRef = rootObj
	}

	// Manual xref + trailer. Approximate (real fixup recomputes
	// ALL offsets); Adobe + most validators accept this for
	// incremental updates.
	xrefStart := int64(buf.Len())
	fmt.Fprintf(&buf, "xref\n")
	if len(objs) > 0 {
		fmt.Fprintf(&buf, "%d %d\n", objs[0].Num, len(objs))
		for _, o := range objs {
			fmt.Fprintf(&buf, "%010d %05d n \n", o.Offset, 0)
		}
	}
	fmt.Fprintf(&buf, "trailer\n<<\n/Size %d\n/Root %d 0 R\n>>\nstartxref\n%d\n%%%%EOF\n",
		maxObj+1, rootRef, xrefStart)
	return buf.Bytes(), nil
}

func encodeStream(content []byte) []byte {
	var w bytes.Buffer
	fmt.Fprintf(&w, "<< /Length %d >>\nstream\n", len(content))
	w.Write(content)
	w.WriteString("\nendstream")
	return w.Bytes()
}

func refList(objs []int) string {
	var w bytes.Buffer
	for i, o := range objs {
		if i > 0 {
			w.WriteByte(' ')
		}
		fmt.Fprintf(&w, "%d 0 R", o)
	}
	return w.String()
}

func bytesToInt(b []byte) int {
	n := 0
	for _, c := range b {
		if c < '0' || c > '9' {
			return n
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// findOriginalCatalog reads /Root from the trailer + extracts the
// catalog object's body bytes. Returns (0, nil, false) on parse
// failure.
func findOriginalCatalog(raw []byte) (int, []byte, bool) {
	m := rootRefRE.FindSubmatch(raw)
	if m == nil {
		return 0, nil, false
	}
	rootNum := bytesToInt(m[1])
	rx := regexp.MustCompile(fmt.Sprintf(`(?s)\b%d\s+0\s+obj\s*(<<.*?>>)\s*endobj`, rootNum))
	body := rx.FindSubmatch(raw)
	if body == nil {
		return rootNum, nil, false
	}
	return rootNum, body[1], true
}

var rootRefRE = regexp.MustCompile(`/Root\s+(\d+)\s+\d+\s+R`)
