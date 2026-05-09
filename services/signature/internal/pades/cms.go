package pades

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"fmt"
	"math/big"
	"time"
)

// cmsSignedData is the trimmed-down ASN.1 we care about. PAdES uses
// detached SignedData (RFC 5652) — no encrypted content, just signed
// attributes + a signature over them.
type cmsSignedData struct {
	Raw asn1.RawContent

	// version ::= Version (CMSVersion)
	Version int
	// digestAlgorithms ::= SET of DigestAlgorithmIdentifier
	DigestAlgorithms []asn1.RawValue `asn1:"set"`
	// encapContentInfo holds eContentType + (omitted) eContent
	EncapContentInfo struct {
		EContentType asn1.ObjectIdentifier
	}
	// certificates is an IMPLICIT [0] SET OF Certificate. We pull
	// the bytes raw and re-parse with x509.ParseCertificates.
	Certificates asn1.RawValue `asn1:"optional,tag:0"`
	// crls — IMPLICIT [1] SET OF CRL. We don't validate against
	// these here (LTV does that downstream); skipped.
	CRLs asn1.RawValue `asn1:"optional,tag:1"`
	// signerInfos ::= SET of SignerInfo
	SignerInfos []signerInfo `asn1:"set"`
}

type signerInfo struct {
	Raw                asn1.RawContent
	Version            int
	IssuerAndSerial    issuerAndSerialNumber
	DigestAlgorithm    asn1.RawValue
	AuthenticatedAttrs asn1.RawValue `asn1:"optional,tag:0"`
	SignatureAlgorithm asn1.RawValue
	Signature          []byte
	UnauthenticatedAttrs asn1.RawValue `asn1:"optional,tag:1"`
}

type issuerAndSerialNumber struct {
	Raw          asn1.RawContent
	IssuerName   asn1.RawValue
	SerialNumber *big.Int
}

// signedAttrSet is the payload that's actually hashed for the
// signature (when authenticated attributes are present, which
// PAdES always does).
type signedAttrSet struct {
	Raw asn1.RawContent
}

// verifyCMS parses a detached SignedData blob, finds the signer
// cert, hashes the signed bytes, and verifies the signature against
// signedAttrs. Returns the signer cert + the chain certs found
// embedded in the CMS so the caller can walk to a trust anchor.
func verifyCMS(cmsBytes, signedDocBytes []byte) (*x509.Certificate, []*x509.Certificate, error) {
	// CMS ContentInfo wraps SignedData: SEQUENCE { contentType OID,
	// content [0] EXPLICIT SignedData }. Strip ContentInfo first.
	var ci struct {
		ContentType asn1.ObjectIdentifier
		Content     asn1.RawValue `asn1:"explicit,tag:0"`
	}
	if _, err := asn1.Unmarshal(cmsBytes, &ci); err != nil {
		return nil, nil, fmt.Errorf("cms ContentInfo: %w", err)
	}
	if !ci.ContentType.Equal(oidSignedData) {
		return nil, nil, fmt.Errorf("cms: contentType %v, want SignedData", ci.ContentType)
	}
	var sd cmsSignedData
	if _, err := asn1.Unmarshal(ci.Content.Bytes, &sd); err != nil {
		return nil, nil, fmt.Errorf("cms SignedData: %w", err)
	}

	// Pull cert chain.
	var chain []*x509.Certificate
	if len(sd.Certificates.Bytes) > 0 {
		// IMPLICIT SET OF — sd.Certificates.Bytes IS the inner DER
		// concatenation of certificates (one or more).
		certs, err := x509.ParseCertificates(sd.Certificates.Bytes)
		if err != nil {
			return nil, nil, fmt.Errorf("cms cert parse: %w", err)
		}
		chain = certs
	}
	if len(sd.SignerInfos) == 0 {
		return nil, chain, errors.New("cms: no SignerInfo")
	}
	si := sd.SignerInfos[0]

	// Find the signer cert by IssuerAndSerial.
	signer := findCertBySerial(chain, si.IssuerAndSerial.SerialNumber)
	if signer == nil {
		return nil, chain, errors.New("cms: signer cert not in CMS chain")
	}

	// Pick the hash function from digestAlgorithm in SignerInfo.
	digestOID, err := readAlgorithmOID(si.DigestAlgorithm.FullBytes)
	if err != nil {
		return signer, chain, fmt.Errorf("cms digest alg: %w", err)
	}
	hashFn, hashID, err := hashByOID(digestOID)
	if err != nil {
		return signer, chain, err
	}

	// Compute the digest the signer signed over. PAdES always uses
	// signedAttrs, which means the signature is over DER(signedAttrs)
	// re-encoded as SET (tag 0x31) — NOT over the document directly.
	if len(si.AuthenticatedAttrs.Bytes) == 0 {
		return signer, chain, errors.New("cms: signedAttrs missing (PAdES requires it)")
	}
	// Re-encode IMPLICIT [0] as explicit SET for hashing.
	signedAttrs := append([]byte{0x31}, si.AuthenticatedAttrs.Bytes[1:]...)
	// Replace the length bytes in case they shift; asn1.Marshal handles it.
	// Easier: marshal the inner content fresh.
	// si.AuthenticatedAttrs.FullBytes is the [0] IMPLICIT encoding;
	// swap the first byte from 0xa0 (context-specific tag 0) to 0x31
	// (universal SET).
	if len(si.AuthenticatedAttrs.FullBytes) > 0 {
		signedAttrs = make([]byte, len(si.AuthenticatedAttrs.FullBytes))
		copy(signedAttrs, si.AuthenticatedAttrs.FullBytes)
		signedAttrs[0] = 0x31
	}

	// Verify the document digest matches messageDigest in signedAttrs.
	docDigest := hashFn(signedDocBytes)
	if !signedAttrsCarryDigest(si.AuthenticatedAttrs.Bytes, docDigest) {
		return signer, chain, errors.New("cms: messageDigest does not match document digest (tampered)")
	}

	// Hash the signedAttrs and verify the signature.
	attrsDigest := hashFn(signedAttrs)
	sigAlgOID, err := readAlgorithmOID(si.SignatureAlgorithm.FullBytes)
	if err != nil {
		return signer, chain, fmt.Errorf("cms sig alg: %w", err)
	}
	if err := verifySignature(signer, sigAlgOID, hashID, attrsDigest, si.Signature); err != nil {
		return signer, chain, fmt.Errorf("cms signature verify: %w", err)
	}
	return signer, chain, nil
}

// readAlgorithmOID pulls the OID out of an AlgorithmIdentifier
// SEQUENCE { OID, ANY OPTIONAL }.
func readAlgorithmOID(der []byte) (asn1.ObjectIdentifier, error) {
	var ai struct {
		OID  asn1.ObjectIdentifier
		Rest asn1.RawValue `asn1:"optional"`
	}
	if _, err := asn1.Unmarshal(der, &ai); err != nil {
		return nil, err
	}
	return ai.OID, nil
}

func hashByOID(oid asn1.ObjectIdentifier) (func([]byte) []byte, crypto.Hash, error) {
	switch {
	case oid.Equal(oidSHA256):
		return func(b []byte) []byte { h := sha256.Sum256(b); return h[:] }, crypto.SHA256, nil
	case oid.Equal(oidSHA384):
		return func(b []byte) []byte { h := sha512.Sum384(b); return h[:] }, crypto.SHA384, nil
	case oid.Equal(oidSHA512):
		return func(b []byte) []byte { h := sha512.Sum512(b); return h[:] }, crypto.SHA512, nil
	}
	return nil, 0, fmt.Errorf("unsupported digest algorithm %v", oid)
}

func verifySignature(cert *x509.Certificate, sigAlgOID asn1.ObjectIdentifier, hashID crypto.Hash, digest, sig []byte) error {
	switch pub := cert.PublicKey.(type) {
	case *rsa.PublicKey:
		switch {
		case sigAlgOID.Equal(oidRSAEncryption), sigAlgOID.Equal(oidSHA256WithRSA), sigAlgOID.Equal(oidSHA384WithRSA), sigAlgOID.Equal(oidSHA512WithRSA):
			return rsa.VerifyPKCS1v15(pub, hashID, digest, sig)
		case sigAlgOID.Equal(oidRSASSAPSS):
			return rsa.VerifyPSS(pub, hashID, digest, sig, nil)
		}
	case *ecdsa.PublicKey:
		if !ecdsa.VerifyASN1(pub, digest, sig) {
			return errors.New("ecdsa verify failed")
		}
		return nil
	}
	return fmt.Errorf("unsupported (key, algorithm) for signature verification: %v", sigAlgOID)
}

// signedAttrsCarryDigest looks for the messageDigest attribute (OID
// 1.2.840.113549.1.9.4) inside the signedAttrs SET body and checks
// its OCTET STRING value matches `want`.
func signedAttrsCarryDigest(setBody, want []byte) bool {
	// Each Attribute is SEQUENCE { OID, SET OF AttributeValue }.
	rest := setBody
	for len(rest) > 0 {
		var attr struct {
			OID    asn1.ObjectIdentifier
			Values asn1.RawValue `asn1:"set"`
		}
		var err error
		rest, err = asn1.Unmarshal(rest, &attr)
		if err != nil {
			return false
		}
		if !attr.OID.Equal(oidMessageDigest) {
			continue
		}
		// Values is a SET — first member is OCTET STRING(want).
		var got []byte
		if _, err := asn1.Unmarshal(attr.Values.Bytes, &got); err != nil {
			return false
		}
		return bytesEqual(got, want)
	}
	return false
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func findCertBySerial(certs []*x509.Certificate, serial *big.Int) *x509.Certificate {
	for _, c := range certs {
		if c.SerialNumber != nil && serial != nil && c.SerialNumber.Cmp(serial) == 0 {
			return c
		}
	}
	return nil
}

// PKCS / X.509 OIDs we touch.
var (
	oidSignedData      = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
	oidMessageDigest   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 4}
	oidSHA256          = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}
	oidSHA384          = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 2}
	oidSHA512          = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 3}
	oidRSAEncryption   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 1}
	oidSHA256WithRSA   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 11}
	oidSHA384WithRSA   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 12}
	oidSHA512WithRSA   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 13}
	oidRSASSAPSS       = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 10}
)

// silence unused import warnings if Go drops one of these
var (
	_ = time.Now
)
