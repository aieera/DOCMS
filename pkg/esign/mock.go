// In-memory deterministic adapter for CI + Playwright.
//
// Production refuses to resolve this provider — boot-time factory
// gates it on a `mock_ok` flag set only in dev/test envs.
package esign

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"
)

type MockClient struct {
	mu sync.Mutex
	envelopes map[string]*mockEnv
}

type mockEnv struct {
	id         string
	tenantID   string
	requestID  string
	recipients []Recipient
	status     string
	createdAt  time.Time
	signedPDF  []byte
}

// NewMock constructs a fresh in-memory client.
func NewMock() *MockClient {
	return &MockClient{envelopes: map[string]*mockEnv{}}
}

func (m *MockClient) Provider() Provider { return ProviderMock }

func (m *MockClient) Send(_ context.Context, req SendReq) (*SendResp, error) {
	if len(req.DocumentBytes) == 0 {
		return nil, errors.New("mock send: document_bytes empty")
	}
	id := "mock-env-" + req.RequestID
	urls := map[string]string{}
	for _, r := range req.Recipients {
		if r.Embedded {
			urls[r.Email] = "/sign/mock-embed?envelope=" + id + "&recipient=" + r.Email
		}
	}
	m.mu.Lock()
	m.envelopes[id] = &mockEnv{
		id: id, tenantID: req.TenantID, requestID: req.RequestID,
		recipients: req.Recipients, status: "in_progress",
		createdAt: time.Now().UTC(),
		signedPDF: append([]byte("MOCK-SIGNED-"), req.DocumentBytes...),
	}
	m.mu.Unlock()
	return &SendResp{
		EnvelopeID: id, Status: "in_progress", SigningURLs: urls,
		CreatedAt: time.Now().UTC(),
	}, nil
}

// CompleteEnvelope is a test-only helper that flips an envelope to
// completed so a follow-up GetSignedDocument returns the bytes.
func (m *MockClient) CompleteEnvelope(envelopeID string) {
	m.mu.Lock()
	if e, ok := m.envelopes[envelopeID]; ok {
		e.status = "completed"
		now := time.Now().UTC()
		for i := range e.recipients {
			e.recipients[i].Role = "signer"
			_ = now // could stamp signedAt — skipped, status pivots in GetStatus
		}
	}
	m.mu.Unlock()
}

func (m *MockClient) GetStatus(_ context.Context, req StatusReq) (*StatusResp, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.envelopes[req.EnvelopeID]
	if !ok {
		return nil, ErrUnknownEnvelope
	}
	resp := &StatusResp{EnvelopeID: e.id, Status: e.status, UpdatedAt: time.Now().UTC()}
	for _, r := range e.recipients {
		st := "pending"
		if e.status == "completed" {
			st = "signed"
			now := time.Now().UTC()
			resp.Recipients = append(resp.Recipients, RecipientStatus{
				Email: r.Email, Status: st, SignedAt: &now,
			})
			continue
		}
		resp.Recipients = append(resp.Recipients, RecipientStatus{
			Email: r.Email, Status: st,
		})
	}
	return resp, nil
}

func (m *MockClient) GetSignedDocument(_ context.Context, req GetSignedReq) (*GetSignedResp, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.envelopes[req.EnvelopeID]
	if !ok {
		return nil, ErrUnknownEnvelope
	}
	out := &GetSignedResp{SignedPDF: e.signedPDF, ContentType: "application/pdf"}
	if req.IncludeCoC {
		out.CoCPDF = []byte("MOCK-COC-" + e.id)
	}
	return out, nil
}

// ParseWebhook accepts any JSON body; no HMAC verification because
// there's no shared secret to verify against. Tests can call this
// directly to drive the post-completion path.
func (m *MockClient) ParseWebhook(_ http.Header, raw []byte) (*WebhookEvent, error) {
	var msg struct {
		EnvelopeID string `json:"envelope_id"`
		EventType  string `json:"event_type"`
		Status     string `json:"status"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil, err
	}
	if msg.EnvelopeID == "" {
		return nil, errors.New("mock webhook: envelope_id required")
	}
	return &WebhookEvent{
		Provider: ProviderMock, EnvelopeID: msg.EnvelopeID,
		EventType: msg.EventType, ExternalID: msg.EnvelopeID + ":" + msg.EventType,
		OccurredAt: time.Now().UTC(), Status: msg.Status, RawPayload: raw,
	}, nil
}

var _ ESignClient = (*MockClient)(nil)
