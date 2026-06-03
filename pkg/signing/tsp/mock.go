// In-memory deterministic adapter for tests + Playwright.
//
// Produces NOT-PAdES-VALID outputs — the SignedHash is just the
// SHA-256 of the document hash xor'd with a fixed key. Plenty for
// "did the redirect dance happen" assertions; useless for actually
// embedding into a PDF that a verifier would accept. Production
// must never resolve this provider — the factory in services/
// signature/internal/qes/factory.go gates it behind a `mock_ok`
// boot flag that's only set in CI.
package tsp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

// MockClient implements TSPClient deterministically.
type MockClient struct {
	mu sync.Mutex
	// transactions remembers Authorize'd ids so Sign can match on
	// them; entries are removed once consumed.
	transactions map[string]mockTxn
	// authCodeOK lets tests force "expired" / "wrong code" by
	// returning a non-empty error string.
	authCodeError string
}

type mockTxn struct {
	subjectID    string
	documentHash string
	expires      time.Time
	authCode     string
}

// NewMock constructs a fresh adapter. SafeOnly returns it; the
// service-layer factory wires it under name "mock".
func NewMock() *MockClient {
	return &MockClient{transactions: map[string]mockTxn{}}
}

// SetAuthCodeError forces Sign to fail with the given error name
// ("expired", "consumed", or anything else for a generic error).
// Used by Playwright to exercise the failure-state UI.
func (m *MockClient) SetAuthCodeError(reason string) {
	m.mu.Lock()
	m.authCodeError = reason
	m.mu.Unlock()
}

func (m *MockClient) Provider() Provider { return ProviderMock }

func (m *MockClient) Register(_ context.Context, req RegisterReq) (*RegisterResp, error) {
	if req.SignerEmail == "" {
		return nil, errors.New("mock register: signer email required")
	}
	return &RegisterResp{SubjectID: "mock:" + req.SignerEmail}, nil
}

func (m *MockClient) Authorize(_ context.Context, req AuthorizeReq) (*AuthorizeResp, error) {
	if req.DocumentHash == "" {
		return nil, errors.New("mock authorize: document_hash required")
	}
	id := "mock-tx-" + req.DocumentHash[:min(8, len(req.DocumentHash))]
	authCode := "MOCK-" + req.DocumentHash[:min(6, len(req.DocumentHash))]
	m.mu.Lock()
	m.transactions[id] = mockTxn{
		subjectID:    req.SubjectID,
		documentHash: req.DocumentHash,
		expires:      time.Now().Add(15 * time.Minute),
		authCode:     authCode,
	}
	m.mu.Unlock()
	// The mock "redirect" is our own /qes/mock-consent page that
	// the e2e test fills in. It points back to req.ReturnURL with
	// `code=<authCode>` so the rest of the flow is realistic.
	return &AuthorizeResp{
		RedirectURL: req.ReturnURL + "&code=" + authCode,
		ExternalID:  id,
		ExpiresAt:   time.Now().Add(15 * time.Minute),
	}, nil
}

func (m *MockClient) Sign(_ context.Context, req SignReq) (*SignResp, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.authCodeError != "" {
		switch m.authCodeError {
		case "expired":
			return nil, ErrSessionExpired
		case "consumed":
			return nil, ErrAlreadyConsumed
		default:
			return nil, fmt.Errorf("mock: %s", m.authCodeError)
		}
	}
	txn, ok := m.transactions[req.ExternalID]
	if !ok {
		return nil, ErrAlreadyConsumed
	}
	if time.Now().After(txn.expires) {
		delete(m.transactions, req.ExternalID)
		return nil, ErrSessionExpired
	}
	if txn.authCode != req.AuthCode {
		return nil, fmt.Errorf("mock sign: bad auth_code")
	}
	delete(m.transactions, req.ExternalID)

	// Deterministic-but-fake signature: SHA-256(doc_hash || subject)
	// base-64'd. Recognisable in test assertions, useless to a
	// verifier.
	h := sha256.New()
	h.Write([]byte(txn.documentHash))
	h.Write([]byte(txn.subjectID))
	signed := h.Sum(nil)

	now := time.Now().UTC()
	return &SignResp{
		SignedHash: signed,
		CertPEM:    mockCertPEM(txn.subjectID),
		ChainPEM:   "",
		SubjectDN:  "CN=" + txn.subjectID + ",O=SeDoc Mock QTSP,C=CH",
		IssuerDN:   "CN=SeDoc Mock Root,O=SeDoc Mock QTSP,C=CH",
		SerialHex:  hex.EncodeToString(signed[:8]),
		NotBefore:  now.Add(-time.Hour),
		NotAfter:   now.Add(365 * 24 * time.Hour),
		LTVRevocation: []byte(`{"ocsp":"mock-ocsp-response"}`),
	}, nil
}

func (m *MockClient) Validate(_ context.Context, req ValidateReq) (*ValidateResp, error) {
	if req.CertPEM == "" {
		return &ValidateResp{Valid: false, Reason: "no cert"}, nil
	}
	return &ValidateResp{Valid: true, OnTrustList: true, Reason: "mock: always valid"}, nil
}

// decodeB64 is shared with the real adapters.
func decodeB64(s string) ([]byte, error) {
	if s == "" {
		return nil, errors.New("empty base64")
	}
	return base64.StdEncoding.DecodeString(s)
}

// mockCertPEM returns a recognisable but invalid PEM block. The
// Validate call is a no-op so this never hits a real x509 parser.
func mockCertPEM(subject string) string {
	return "-----BEGIN CERTIFICATE-----\nMOCK-" + subject + "\n-----END CERTIFICATE-----"
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var _ TSPClient = (*MockClient)(nil)
