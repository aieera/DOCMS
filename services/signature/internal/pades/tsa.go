package pades

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/asn1"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"time"
)

// TSAClient is what Embedder uses to fetch RFC 3161 timestamps.
// Pluggable so tests can swap in a deterministic double.
type TSAClient interface {
	Stamp(ctx context.Context, sha256Digest []byte) (*TSAToken, error)
}

// TSAToken is the parsed-just-enough view of an RFC 3161
// TimeStampToken. We keep RawToken for embedding and surface
// GenTime + the signer's cert for chain validation downstream.
type TSAToken struct {
	GenTime  time.Time
	RawToken []byte // DER ContentInfo wrapping the SignedData
}

// HTTPTSAClient implements TSAClient against a vanilla RFC 3161
// HTTP responder.
type HTTPTSAClient struct {
	URL  string
	HTTP *http.Client
}

// NewHTTPTSAClient builds a client. URL is required; nil HTTP
// falls back to http.DefaultClient.
func NewHTTPTSAClient(url string, hc *http.Client) (*HTTPTSAClient, error) {
	if url == "" {
		return nil, errors.New("tsa: URL required")
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	return &HTTPTSAClient{URL: url, HTTP: hc}, nil
}

// timeStampReq mirrors RFC 3161 §2.4.1.
type timeStampReq struct {
	Version        int
	MessageImprint messageImprint
	ReqPolicy      asn1.ObjectIdentifier `asn1:"optional"`
	Nonce          *big.Int              `asn1:"optional"`
	CertReq        bool                  `asn1:"optional,default:false"`
}

type messageImprint struct {
	HashAlgorithm asn1.RawValue
	HashedMessage []byte
}

// timeStampResp matches RFC 3161 §2.4.2.
type timeStampResp struct {
	Status struct {
		Status int
	}
	TimeStampToken asn1.RawValue `asn1:"optional"`
}

// Stamp issues a TSA request for the given SHA-256 digest and
// returns the token.
func (c *HTTPTSAClient) Stamp(ctx context.Context, digest []byte) (*TSAToken, error) {
	if len(digest) != 32 {
		return nil, fmt.Errorf("tsa: digest must be 32 bytes (SHA-256), got %d", len(digest))
	}
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return nil, err
	}
	nonce := new(big.Int).SetBytes(nonceBytes)

	hashAlg, err := asn1.Marshal(struct {
		OID  asn1.ObjectIdentifier
		Null asn1.RawValue
	}{
		OID:  oidSHA256,
		Null: asn1.RawValue{Tag: asn1.TagNull},
	})
	if err != nil {
		return nil, err
	}
	req := timeStampReq{
		Version: 1,
		MessageImprint: messageImprint{
			HashAlgorithm: asn1.RawValue{FullBytes: hashAlg},
			HashedMessage: digest,
		},
		Nonce:   nonce,
		CertReq: true,
	}
	reqDER, err := asn1.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("tsa marshal: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.URL, bytes.NewReader(reqDER))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/timestamp-query")
	httpReq.Header.Set("Accept", "application/timestamp-reply")
	httpResp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTSA, err)
	}
	defer func() { _ = httpResp.Body.Close() }()
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ErrTSA, httpResp.StatusCode)
	}
	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("tsa read: %w", err)
	}
	var resp timeStampResp
	if _, err := asn1.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("tsa decode: %w", err)
	}
	if resp.Status.Status != 0 && resp.Status.Status != 1 {
		// 0 = granted, 1 = grantedWithMods. Anything else is reject.
		return nil, fmt.Errorf("%w: tsa status %d", ErrTSA, resp.Status.Status)
	}
	if len(resp.TimeStampToken.FullBytes) == 0 {
		return nil, fmt.Errorf("%w: empty token", ErrTSA)
	}

	// Pull genTime out of the token's TSTInfo. We don't fully
	// re-verify the token's signature here — the embedder will
	// add it to the PDF; the Verifier's pass over the resulting
	// document does the chain walk.
	gen, _ := extractGenTime(resp.TimeStampToken.FullBytes)
	return &TSAToken{GenTime: gen, RawToken: resp.TimeStampToken.FullBytes}, nil
}

// extractGenTime pulls TSTInfo.genTime from a TimeStampToken without
// fully re-parsing the SignedData. Best-effort: returns zero time
// on parse failure rather than erroring; the caller treats zero as
// "TSA token present but unparseable" and falls back to time.Now().
func extractGenTime(token []byte) (time.Time, error) {
	// TimeStampToken is ContentInfo { ct = signed-data, content [0] = SignedData }
	// SignedData.encapContentInfo.eContent = OCTET STRING(TSTInfo)
	// TSTInfo = SEQUENCE { ..., genTime GeneralizedTime, ... }
	//
	// Rather than walk the whole structure we scan for a
	// GeneralizedTime tag (0x18) inside the body — RFC 3161 only
	// emits one. Robust enough for our diagnostic use.
	for i := 0; i < len(token)-3; i++ {
		if token[i] != 0x18 {
			continue
		}
		l := int(token[i+1])
		if l < 14 || l > 32 || i+2+l > len(token) {
			continue
		}
		// GeneralizedTime format: YYYYMMDDHHMMSS[.fff]Z
		s := string(token[i+2 : i+2+l])
		t, err := time.Parse("20060102150405Z", s)
		if err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, errors.New("tsa: genTime not found in token")
}

// hashForTSA returns the digest the embedder should send to the
// TSA. PAdES-B-T timestamps the signature itself (not the document
// digest) — that's how a B-T chain proves "the signature existed
// at this point in time".
func hashForTSA(cmsBytes []byte) []byte {
	h := sha256.Sum256(cmsBytes)
	return h[:]
}
