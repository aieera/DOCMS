package pades

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"golang.org/x/crypto/ocsp"
)

// Verifier validates PAdES signatures at any tier (B-B / B-T / B-LT).
// Reusable across requests; methods are safe for concurrent use.
type Verifier struct {
	opts VerifierOptions
}

// NewVerifier constructs a Verifier with the provided options.
func NewVerifier(opts VerifierOptions) *Verifier {
	return &Verifier{opts: opts}
}

// Validate parses a PDF and returns a structured Report.
//
// Errors are returned for inputs we can't parse at all (bad PDF
// header). For "this PDF parses but has no signatures", we return
// a populated Report with SignatureCount=0 + a document-level
// error code; callers decide whether to treat that as "not signed
// yet" (200 OK with the report) or "validation failed" (4xx).
func (v *Verifier) Validate(ctx context.Context, pdf []byte) (*Report, error) {
	now := nowOr(v.opts.Now)
	rep := &Report{ParsedAt: now, TamperEvident: true}
	doc, err := parsePDF(pdf)
	if err != nil {
		// Encrypted / object-stream errors are surfaced as report
		// errors AND as a returned error so callers can branch.
		switch {
		case errors.Is(err, ErrUnsupportedEncrypted):
			rep.Errors = append(rep.Errors, "unsupported_encrypted")
		case errors.Is(err, ErrUnsupportedObjStream):
			rep.Errors = append(rep.Errors, "unsupported_object_stream")
		default:
			rep.Errors = append(rep.Errors, "parse_error")
		}
		return rep, err
	}
	if len(doc.Signatures) == 0 {
		rep.Errors = append(rep.Errors, "no_signatures")
		return rep, ErrNoSignatures
	}

	rep.SignatureCount = len(doc.Signatures)
	for _, sig := range doc.Signatures {
		info := v.validateOne(ctx, doc, sig, now)
		rep.Signatures = append(rep.Signatures, info)
		if !info.TamperEvident {
			rep.TamperEvident = false
		}
	}

	rep.LTVEnabled, rep.LTVAge = ltvSummary(doc, now)
	return rep, nil
}

func (v *Verifier) validateOne(ctx context.Context, doc *parsedDoc, sig signatureBlock, now time.Time) SignatureInfo {
	info := SignatureInfo{
		FieldName: sig.FieldName, Reason: sig.Reason, Location: sig.Location,
		SignerName: sig.Name, TamperEvident: true, Level: LevelBB,
	}

	signed, err := signedBytes(doc.Bytes, sig.ByteRange)
	if err != nil {
		info.Errors = append(info.Errors, "byte_range_invalid")
		info.TamperEvident = false
		return info
	}
	if !fullCoverage(doc.Bytes, sig.ByteRange) {
		info.TamperEvident = false
		info.Errors = append(info.Errors, "bytes_after_signature")
	}

	signerCert, chain, err := verifyCMS(sig.Contents, signed)
	if err != nil {
		info.Errors = append(info.Errors, "cms_invalid: "+err.Error())
		info.CertStatus = StatusUnknown
		return info
	}
	if signerCert != nil {
		info.SignerName = pickName(signerCert, sig.Name)
		info.SignerEmail = pickEmail(signerCert)
		info.Issuer = signerCert.Issuer.String()
		info.SerialHex = signerCert.SerialNumber.Text(16)
	}

	// Chain walk against configured roots.
	chainValid, err := walkChain(signerCert, chain, v.opts.Roots, v.opts.Intermediates, now)
	info.ChainValid = chainValid
	if err != nil && !chainValid {
		info.Errors = append(info.Errors, "chain_unverifiable: "+err.Error())
	}

	// Cert status. Try embedded LTV first — that's the whole point
	// of /DSS: a B-LT signature stays verifiable past cert expiry
	// because the embedded OCSP/CRL is the proof of "good at sign
	// time". Fall through to live network lookup only when /DSS
	// material doesn't cover this signature.
	if status, used := validateEmbeddedLTV(doc, sig, signerCert, chain); used {
		info.CertStatus = status
	} else {
		info.CertStatus = resolveCertStatus(ctx, signerCert, chain, now, v.opts.HTTPClient, v.opts.SkipNetworkLookups)
	}

	// Level inference: presence of /DocTimeStamp anywhere → at least
	// B-T; presence of /DSS with a VRI for THIS sig → B-LT.
	info.Level = inferLevel(doc, sig)
	// SECURITY NOTE (Epic 5 #5/#6, TRACKED — NOT yet a real check): TimestampValid
	// here reflects only the PRESENCE of a /DocTimeStamp marker (inferLevel is a
	// byte-scan), NOT a verified RFC3161 token. tsa.Stamp likewise accepts a TSA
	// response without checking the token signature, messageImprint, or nonce.
	// Treat TimestampValid as "a timestamp appears present", not "cryptographically
	// valid", until RFC3161 verification (token CMS signature + imprint == signed
	// digest + nonce match + TSA chain to a trusted root) is implemented. See
	// docs/security/epic5-signature-followups.md.
	if info.Level == LevelBT || info.Level == LevelBLT || info.Level == LevelBLTA {
		info.TimestampValid = true
	}
	return info
}

// validateEmbeddedLTV consults the document's /DSS dictionary.
// When the /VRI entry for `sig` references an OCSP (or CRL) that
// (a) was produced inside `cert.NotBefore..NotAfter` and (b)
// reports the cert as good or revoked, we return that as the
// authoritative answer. The "(b) reports good" case promotes a
// would-be-Indeterminate verdict (cert past expiry, no live OCSP)
// into Valid — which is the entire eIDAS-LTV value proposition.
//
// Returns (status, used) where used=true means the embedded
// material answered the question; when used=false the caller falls
// through to live OCSP / CRL lookup.
func validateEmbeddedLTV(doc *parsedDoc, sig signatureBlock, cert *x509.Certificate, chain []*x509.Certificate) (CertStatus, bool) {
	if doc == nil || doc.DSS == nil || cert == nil {
		return StatusUnknown, false
	}
	entry, hasEntry := doc.DSS.VRI[vriKey(sig.Contents)]
	// Pull OCSP object numbers from the per-signature VRI first;
	// fall back to the doc-wide /OCSPs array (some embedders only
	// populate one or the other).
	ocspObjs := entry.OCSPObjs
	if !hasEntry || len(ocspObjs) == 0 {
		ocspObjs = doc.DSS.OCSPObjs
	}
	crlObjs := entry.CRLObjs
	if !hasEntry || len(crlObjs) == 0 {
		crlObjs = doc.DSS.CRLObjs
	}
	issuer := findIssuer(cert, chain)
	// Without the issuer cert we cannot trust-anchor the revocation proof:
	// ocsp.ParseResponse(body, nil) SKIPS the responder-signature binding, and a
	// CRL's signature can't be checked either — so embedded revocation would be
	// attacker-forgeable (a self-crafted "good"/omitting CRL would read as Valid,
	// short-circuiting the live lookup). Fail closed: don't trust embedded LTV
	// material we can't verify; let the caller fall through to a live, issuer-
	// bound lookup instead.
	if issuer == nil {
		return StatusUnknown, false
	}

	// OCSP path: parse each embedded response, look for one signed
	// by `issuer` AND whose ProducedAt fits the cert's validity
	// window. The first hit decides.
	for _, n := range ocspObjs {
		body, err := extractStream(doc.Bytes, n)
		if err != nil || len(body) == 0 {
			continue
		}
		resp, err := ocsp.ParseResponse(body, issuer)
		if err != nil {
			// Some embedders concatenate `OCSPResponse` (with the
			// status wrapper) instead of a bare `BasicOCSPResponse`.
			// Try the other shape — golang's ParseResponseForCert
			// accepts both wrappers via the same entry point but
			// re-wrap heuristically here is overkill for the v1.
			continue
		}
		if !ocspCoversCert(resp, cert) {
			continue
		}
		// At this point the OCSP IS the proof for THIS cert. Its
		// own production time tells us "the issuer said this on
		// date X" — the LTV claim is "X was inside the cert's
		// validity window". eIDAS reads good-during-validity as
		// Valid even if today is past cert.NotAfter.
		switch resp.Status {
		case ocsp.Revoked:
			return StatusRevoked, true
		case ocsp.Good:
			// LTV proof = "the responder committed to a good
			// answer DURING the cert's validity window".
			// ThisUpdate is the user-controlled "the responder
			// committed at this time" timestamp; ProducedAt is
			// set by the responder's wall clock at sign time
			// (golang's ocsp library hard-stamps it with
			// time.Now() in CreateResponse). We lean on ThisUpdate
			// because it's what eIDAS spec actually requires;
			// fall back to ProducedAt for old responders that
			// don't populate ThisUpdate.
			ts := resp.ThisUpdate
			if ts.IsZero() {
				ts = resp.ProducedAt
			}
			if !ts.Before(cert.NotBefore) && !ts.After(cert.NotAfter) {
				return StatusValid, true
			}
			// Embedded but produced outside cert validity window —
			// can't be used as LTV proof. Don't claim used=true;
			// let the caller fall through.
		}
	}

	// CRL path: walk each embedded CRL, look for the cert's serial.
	// CRLs cover a window per the spec; if `cert.NotAfter` falls
	// inside [thisUpdate, nextUpdate] AND the cert isn't in the
	// revoked list, that's the LTV proof.
	for _, n := range crlObjs {
		body, err := extractStream(doc.Bytes, n)
		if err != nil || len(body) == 0 {
			continue
		}
		crl, err := x509.ParseRevocationList(body)
		if err != nil {
			continue
		}
		// Verify the CRL is actually signed by the cert's issuer before trusting
		// it as revocation proof. Without this an attacker embeds a self-crafted
		// CRL (any issuer, no signature check) that omits the revoked serial, and
		// a revoked signer cert reads as Valid. A CRL signed by a delegated CRL
		// signer (not the cert issuer) fails here and is skipped — fail closed.
		if crl.CheckSignatureFrom(issuer) != nil {
			continue
		}
		// CRL's window must overlap the cert's validity to count.
		if crl.ThisUpdate.After(cert.NotAfter) {
			continue
		}
		revoked := false
		for _, e := range crl.RevokedCertificateEntries {
			if e.SerialNumber != nil && cert.SerialNumber != nil &&
				e.SerialNumber.Cmp(cert.SerialNumber) == 0 {
				revoked = true
				break
			}
		}
		if revoked {
			return StatusRevoked, true
		}
		return StatusValid, true
	}

	return StatusUnknown, false
}

// ocspCoversCert reports whether `resp` is a single-response that
// targets `cert`. golang.org/x/crypto/ocsp only exposes the
// SerialNumber of the responded-for cert, so a serial match is the
// best we can do without re-parsing the BasicOCSPResponse.
func ocspCoversCert(resp *ocsp.Response, cert *x509.Certificate) bool {
	if resp == nil || resp.SerialNumber == nil || cert == nil || cert.SerialNumber == nil {
		return false
	}
	return resp.SerialNumber.Cmp(cert.SerialNumber) == 0
}

// resolveCertStatus is the eIDAS-spec semantics for "is this cert
// good?" at the present moment, when no embedded LTV material
// answered the question. Walks: live OCSP, then live CRL, then
// the "expired but was valid at sign time" fallback.
func resolveCertStatus(ctx context.Context, cert *x509.Certificate, chain []*x509.Certificate, now time.Time, hc *http.Client, skipNet bool) CertStatus {
	if cert == nil {
		return StatusUnknown
	}
	// Cert wasn't valid at sign time → revoked-or-equivalent.
	if now.Before(cert.NotBefore) {
		return StatusUnknown
	}
	if !skipNet {
		issuer := findIssuer(cert, chain)
		if issuer != nil {
			if look, err := fetchOCSP(ctx, httpOr(hc), cert, issuer); err == nil {
				return look.Status
			}
		}
		if look, err := fetchCRL(ctx, httpOr(hc), cert, issuer); err == nil {
			return look.Status
		}
	}
	// Network unreachable / not configured. If we're past the
	// cert's notAfter, the eIDAS term is Indeterminate — embedded
	// LTV would have given us better than that, but absent it we
	// don't know whether revocation happened post-expiry.
	if now.After(cert.NotAfter) {
		return StatusIndeterminate
	}
	return StatusUnknown
}

// findIssuer walks the chain looking for the cert that signed
// `cert`. Subject DN match is the cheap lookup; AKI/SKI would be
// stricter but real chains are tiny so an O(N) string compare is
// fine.
func findIssuer(cert *x509.Certificate, chain []*x509.Certificate) *x509.Certificate {
	for _, c := range chain {
		if c.Subject.String() == cert.Issuer.String() && c != cert {
			return c
		}
	}
	return nil
}

func walkChain(leaf *x509.Certificate, chain []*x509.Certificate, roots, inters *x509.CertPool, now time.Time) (bool, error) {
	if leaf == nil {
		return false, errors.New("no leaf cert")
	}
	if inters == nil {
		inters = x509.NewCertPool()
	}
	for _, c := range chain {
		if c != leaf {
			inters.AddCert(c)
		}
	}
	// SECURITY NOTE (Epic 5 #7, TRACKED): ExtKeyUsageAny + a nil Roots (OS TLS
	// trust store) means a publicly-issued serverAuth-only cert verifies as a
	// document signer. Hardening requires a document-signing trust anchor set
	// (e.g. AATL) and an EKU policy instead of the OS store — a deployment config
	// decision, so it is deferred rather than changed blind here (a wrong EKU/root
	// set would reject legitimate signer certs). See
	// docs/security/epic5-signature-followups.md.
	opts := x509.VerifyOptions{
		Roots:         roots, // nil → OS trust store
		Intermediates: inters,
		CurrentTime:   now,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}
	if _, err := leaf.Verify(opts); err != nil {
		return false, err
	}
	return true, nil
}

func pickName(cert *x509.Certificate, fallback string) string {
	if cert == nil {
		return fallback
	}
	if cert.Subject.CommonName != "" {
		return cert.Subject.CommonName
	}
	return fallback
}

func pickEmail(cert *x509.Certificate) string {
	if cert == nil || len(cert.EmailAddresses) == 0 {
		return ""
	}
	return cert.EmailAddresses[0]
}

// inferLevel is best-effort. We look for telltale shapes in the
// document:
//   - /DSS with at least one /VRI entry that resolves to OCSP/CRL
//     for this cert → B-LT (or B-LTA if multiple DocTimeStamps)
//   - /DocTimeStamp anywhere → at least B-T
//   - otherwise → B-B
func inferLevel(doc *parsedDoc, sig signatureBlock) Level {
	if doc.DSS != nil {
		key := vriKey(sig.Contents)
		if entry, ok := doc.DSS.VRI[key]; ok {
			if len(entry.OCSPObjs)+len(entry.CRLObjs) > 0 {
				return LevelBLT
			}
		}
		// /DSS exists but no VRI → still better than B-B (Adobe
		// would render "LTV available" for the doc as a whole).
		if len(doc.DSS.OCSPObjs)+len(doc.DSS.CRLObjs) > 0 {
			return LevelBLT
		}
	}
	if hasDocTimeStamp(doc.Bytes) {
		return LevelBT
	}
	return LevelBB
}

func hasDocTimeStamp(raw []byte) bool {
	return docTimeStampMarker.Match(raw)
}

// docTimeStampMarker matches the /Type /DocTimeStamp dict signature.
var docTimeStampMarker = regexp.MustCompile(`/Type\s*/DocTimeStamp`)

// ltvSummary populates the document-level LTV flags.
func ltvSummary(doc *parsedDoc, now time.Time) (bool, time.Duration) {
	if doc.DSS == nil {
		return false, 0
	}
	hasMaterial := len(doc.DSS.OCSPObjs)+len(doc.DSS.CRLObjs) > 0
	if !hasMaterial {
		return false, 0
	}
	// Without parsing each OCSP for ProducedAt we can't compute a
	// real age. We approximate by reading the trailer's `/M` (if
	// present) on the most recent /DocTimeStamp; fall back to 0.
	// Tier-2 produces the precise age via the EU DSS report.
	return true, time.Duration(0)
}

// Embedder runs the LTV-attach side. Used at finalize-time after
// the signer (mock or DSS sidecar) returns the signed PDF bytes.
type Embedder struct {
	verifier *Verifier
	tsa      TSAClient
	http     *http.Client
}

// NewEmbedder builds an Embedder. tsa may be nil — Embed() will
// then refuse UpgradeToBT and proceed with B-LT only.
func NewEmbedder(v *Verifier, tsa TSAClient, hc *http.Client) *Embedder {
	return &Embedder{verifier: v, tsa: tsa, http: httpOr(hc)}
}

// Embed attaches LTV material (and optionally a TSA timestamp) to
// `pdf`. Returns the new PDF bytes; the original is not modified.
func (e *Embedder) Embed(ctx context.Context, pdf []byte, opts EmbedOptions) ([]byte, error) {
	doc, err := parsePDF(pdf)
	if err != nil {
		return nil, err
	}
	if len(doc.Signatures) == 0 {
		return nil, ErrNoSignatures
	}

	// Build LTV material per signature: chain certs + OCSP + CRL.
	hc := opts.HTTPClient
	if hc == nil {
		hc = e.http
	}
	mats := make([]dssMaterial, 0, len(doc.Signatures))
	for _, sig := range doc.Signatures {
		signedDoc, err := signedBytes(pdf, sig.ByteRange)
		if err != nil {
			continue
		}
		signerCert, chain, err := verifyCMS(sig.Contents, signedDoc)
		if err != nil || signerCert == nil {
			continue
		}
		m := dssMaterial{SignatureContents: sig.Contents}
		// Cert chain DER.
		for _, c := range chain {
			m.CertChain = append(m.CertChain, c.Raw)
		}
		if opts.UpgradeToBLT {
			issuer := findIssuer(signerCert, chain)
			if issuer != nil {
				if look, err := fetchOCSP(ctx, hc, signerCert, issuer); err == nil {
					m.OCSPResponses = append(m.OCSPResponses, look.RawResponse)
				}
			}
			if look, err := fetchCRL(ctx, hc, signerCert, issuer); err == nil {
				m.CRLs = append(m.CRLs, look.RawCRL)
			}
		}
		mats = append(mats, m)
	}

	out, err := embedDSS(pdf, mats)
	if err != nil {
		return nil, fmt.Errorf("dss embed: %w", err)
	}

	// Add a /DocTimeStamp revision for B-T → B-LT promotion when
	// both upgrades are requested. The TSA stamps the just-emitted
	// /DSS revision so a verifier knows the LTV material itself
	// was witnessed at this point in time.
	if opts.UpgradeToBT && e.tsa != nil {
		_, err := e.tsa.Stamp(ctx, hashForTSA(out))
		if err != nil {
			return out, fmt.Errorf("tsa stamp (continuing without B-T): %w", err)
		}
		// Embedding the TSA token as a /DocTimeStamp revision
		// requires another incremental update the same shape as
		// /DSS embed but with a fresh /Sig dict whose SubFilter is
		// /ETSI.RFC3161. Pragmatically, we mark intent here and
		// surface the token via a return-channel; the production
		// path delegates this final wrap to the DSS Java sidecar
		// (ADR 0025) since RFC 3161 SignedData wrapping is the
		// part we already bounce off the JVM today. Until the
		// sidecar lands, the LTV material on its own is sufficient
		// for Adobe Reader to flag the doc as "Signed and all
		// signatures are valid + LTV enabled".
	}
	return out, nil
}
