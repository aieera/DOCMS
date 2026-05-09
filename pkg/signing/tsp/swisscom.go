// Swisscom AIS adapter.
//
// API docs: https://documents.swisscom.com/product/1000255-Digital_Signing_Service/Documents/Reference_Guide/Reference_Guide-All-in-Signing-Service-en.pdf
//
// Swisscom uses mTLS (a customer certificate is the auth, no
// OAuth) + a request-signature ceremony built on `OnDemand` and
// `Static` profiles. We only implement OnDemand here — that's the
// QES-with-redirect flow; the Static profile is for stamps with
// pre-vetted signers and isn't on the §11.1 deliverables.
//
// Sandbox: https://ais.swisscom.com/AIS-Server/rs   (mTLS-gated)
package tsp

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// SwisscomConfig — credential bundle. CustomerID identifies us at
// Swisscom; ClientCertPEM / ClientKeyPEM are the mTLS material
// rotated via the runbook. BaseURL points at sandbox or prod.
type SwisscomConfig struct {
	BaseURL       string
	CustomerID    string
	ClientCertPEM string
	ClientKeyPEM  string
	// HTTPClient lets tests inject httptest.NewServer; nil →
	// adapter builds an mTLS client from the cert/key pair.
	HTTPClient *http.Client
}

// SwisscomClient implements TSPClient against AIS.
type SwisscomClient struct {
	cfg  SwisscomConfig
	http *http.Client
}

// NewSwisscom constructs the adapter. Returns ErrNotConfigured if
// any required field is missing; adapter still satisfies the
// interface so the factory can return it for typed errors.
func NewSwisscom(cfg SwisscomConfig) (*SwisscomClient, error) {
	if cfg.BaseURL == "" || cfg.CustomerID == "" {
		return nil, fmt.Errorf("%w: swisscom base_url + customer_id required", ErrNotConfigured)
	}
	hc := cfg.HTTPClient
	if hc == nil {
		if cfg.ClientCertPEM == "" || cfg.ClientKeyPEM == "" {
			return nil, fmt.Errorf("%w: swisscom mTLS client cert + key required", ErrNotConfigured)
		}
		cert, err := tls.X509KeyPair([]byte(cfg.ClientCertPEM), []byte(cfg.ClientKeyPEM))
		if err != nil {
			return nil, fmt.Errorf("swisscom: load client cert: %w", err)
		}
		hc = &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					Certificates: []tls.Certificate{cert},
					RootCAs:      x509.NewCertPool(), // system roots; empty pool == system pool
					MinVersion:   tls.VersionTLS12,
				},
			},
		}
	}
	return &SwisscomClient{cfg: cfg, http: hc}, nil
}

func (c *SwisscomClient) Provider() Provider { return ProviderSwisscom }

// Register is a no-op for Swisscom OnDemand — the signer DN is
// minted at sign time from the email + national-eID claim. We
// return the email as the SubjectID so the rest of the ceremony
// has a stable reference.
func (c *SwisscomClient) Register(_ context.Context, req RegisterReq) (*RegisterResp, error) {
	if req.SignerEmail == "" {
		return nil, fmt.Errorf("swisscom register: signer email required")
	}
	return &RegisterResp{SubjectID: req.SignerEmail}, nil
}

// Authorize calls AIS /sign with the StepUp profile so AIS returns
// a `consentURL` we hand to the browser. The auth code arrives on
// our return-URL once the user completes MobileID / SMS-PIN.
func (c *SwisscomClient) Authorize(ctx context.Context, req AuthorizeReq) (*AuthorizeResp, error) {
	body := map[string]any{
		"SignRequest": map[string]any{
			"@RequestID":     req.SubjectID,
			"@Profile":       "http://ais.swisscom.ch/1.1",
			"OptionalInputs": map[string]any{
				"ClaimedIdentity":      map[string]any{"Name": c.cfg.CustomerID + ":OnDemand-Qualified"},
				"SignatureType":        "urn:ietf:rfc:3369",
				"AdditionalProfile":    "urn:com:swisscom:dss:v1.0:profiles:async",
				"SignatureStandard":    "PAdES",
				"AddTimestamp":         map[string]any{"@Type": "urn:ietf:rfc:3161"},
				"StepUpAuthorisation":  map[string]any{
					"@Type": "urn:com:swisscom:dss:v1.0:resources:StepUp",
					"Phone": req.SignerEmail, // sandbox accepts email; prod is MSISDN
					"Message": req.Reason,
					"ConsentURLCallback": req.ReturnURL,
				},
			},
			"InputDocuments": map[string]any{
				"DocumentHash": map[string]any{
					"@ID":      "doc-1",
					"DigestMethod": map[string]any{"@Algorithm": "http://www.w3.org/2001/04/xmlenc#sha256"},
					"DigestValue":  req.DocumentHash,
				},
			},
		},
	}
	var resp struct {
		AsyncResponse struct {
			ResponseID  string `json:"ResponseID"`
			ConsentURL  string `json:"ConsentURL"`
			ExpiresAt   string `json:"ExpiresAt"`
		} `json:"AsyncResponse"`
		Result struct {
			ResultMajor string `json:"ResultMajor"`
			ResultMinor string `json:"ResultMinor"`
		} `json:"Result"`
	}
	if err := c.do(ctx, "POST", "/sign", body, &resp); err != nil {
		return nil, err
	}
	if resp.Result.ResultMajor != "" && !contains(resp.Result.ResultMajor, "Pending") {
		return nil, fmt.Errorf("swisscom authorize: %s", resp.Result.ResultMinor)
	}
	expires, _ := time.Parse(time.RFC3339, resp.AsyncResponse.ExpiresAt)
	if expires.IsZero() {
		expires = time.Now().Add(15 * time.Minute)
	}
	return &AuthorizeResp{
		RedirectURL: resp.AsyncResponse.ConsentURL,
		ExternalID:  resp.AsyncResponse.ResponseID,
		ExpiresAt:   expires,
	}, nil
}

// Sign polls AIS /pending with the ResponseID. Swisscom returns the
// CMS bytes + signing cert in one call once the user has completed
// the StepUp.
func (c *SwisscomClient) Sign(ctx context.Context, req SignReq) (*SignResp, error) {
	body := map[string]any{
		"PendingRequest": map[string]any{
			"@RequestID": req.ExternalID,
			"OptionalInputs": map[string]any{
				"AsyncResponseID": req.ExternalID,
				"AuthCode":        req.AuthCode,
			},
		},
	}
	var resp struct {
		SignResponse struct {
			SignatureObject struct {
				Base64Signature struct {
					Value string `json:"$"`
				} `json:"Base64Signature"`
			} `json:"SignatureObject"`
			OptionalOutputs struct {
				X509Certificate     string `json:"X509Certificate"`
				X509CertificateChain string `json:"X509CertificateChain"`
				OCSPResponse        string `json:"OCSPResponse"`
				SubjectDN           string `json:"SubjectDN"`
				IssuerDN            string `json:"IssuerDN"`
				SerialNumber        string `json:"SerialNumber"`
				NotBefore           string `json:"NotBefore"`
				NotAfter            string `json:"NotAfter"`
			} `json:"OptionalOutputs"`
			Result struct {
				ResultMajor string `json:"ResultMajor"`
				ResultMinor string `json:"ResultMinor"`
			} `json:"Result"`
		} `json:"SignResponse"`
	}
	if err := c.do(ctx, "POST", "/pending", body, &resp); err != nil {
		return nil, err
	}
	if !contains(resp.SignResponse.Result.ResultMajor, "Success") {
		if contains(resp.SignResponse.Result.ResultMinor, "Expired") {
			return nil, ErrSessionExpired
		}
		return nil, fmt.Errorf("swisscom sign: %s / %s", resp.SignResponse.Result.ResultMajor, resp.SignResponse.Result.ResultMinor)
	}
	signed, err := base64.StdEncoding.DecodeString(resp.SignResponse.SignatureObject.Base64Signature.Value)
	if err != nil {
		return nil, fmt.Errorf("swisscom sign: decode signature: %w", err)
	}
	notBefore, _ := time.Parse(time.RFC3339, resp.SignResponse.OptionalOutputs.NotBefore)
	notAfter, _ := time.Parse(time.RFC3339, resp.SignResponse.OptionalOutputs.NotAfter)
	ocsp, _ := json.Marshal(map[string]string{"ocsp": resp.SignResponse.OptionalOutputs.OCSPResponse})
	return &SignResp{
		SignedHash:    signed,
		CertPEM:       resp.SignResponse.OptionalOutputs.X509Certificate,
		ChainPEM:      resp.SignResponse.OptionalOutputs.X509CertificateChain,
		SubjectDN:     resp.SignResponse.OptionalOutputs.SubjectDN,
		IssuerDN:      resp.SignResponse.OptionalOutputs.IssuerDN,
		SerialHex:     resp.SignResponse.OptionalOutputs.SerialNumber,
		NotBefore:     notBefore,
		NotAfter:      notAfter,
		LTVRevocation: ocsp,
	}, nil
}

// Validate hits AIS /validate with the cert. Swisscom is on the
// EU LOTL so a positive answer here doubles as on-trust-list.
func (c *SwisscomClient) Validate(ctx context.Context, req ValidateReq) (*ValidateResp, error) {
	body := map[string]any{
		"VerifyRequest": map[string]any{
			"InputDocuments": map[string]any{"Certificate": req.CertPEM},
		},
	}
	var resp struct {
		Result struct {
			ResultMajor string `json:"ResultMajor"`
			ResultMinor string `json:"ResultMinor"`
		} `json:"Result"`
	}
	if err := c.do(ctx, "POST", "/validate", body, &resp); err != nil {
		return nil, err
	}
	valid := contains(resp.Result.ResultMajor, "Success")
	return &ValidateResp{
		Valid:       valid,
		Reason:      resp.Result.ResultMinor,
		OnTrustList: valid,
	}, nil
}

func (c *SwisscomClient) do(ctx context.Context, method, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, c.cfg.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("%w: %s %s: %v", ErrTransport, method, path, err)
	}
	defer func() { _ = httpResp.Body.Close() }()
	if httpResp.StatusCode == http.StatusUnauthorized || httpResp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("%w: status %d", ErrUnauthorized, httpResp.StatusCode)
	}
	if httpResp.StatusCode >= 500 {
		return fmt.Errorf("%w: status %d", ErrTransport, httpResp.StatusCode)
	}
	if err := json.NewDecoder(httpResp.Body).Decode(out); err != nil {
		return fmt.Errorf("swisscom: decode: %w", err)
	}
	return nil
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

var _ TSPClient = (*SwisscomClient)(nil)

// silence unused
var _ = errors.New
