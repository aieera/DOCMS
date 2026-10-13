//go:build integration

// S-1 / DEC-17: e-sign send must load and authorize the request, refuse a
// request that is no longer open (or already sent), and send the bytes of
// the request's own stored version — never client-supplied bytes or
// recipients.
package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/esign"
	"github.com/aieera/sedoc/services/signature/internal/model"
)

type fakeESignClient struct {
	sends []esign.SendReq
}

func (f *fakeESignClient) Provider() esign.Provider { return esign.ProviderDocuSign }
func (f *fakeESignClient) Send(_ context.Context, req esign.SendReq) (*esign.SendResp, error) {
	f.sends = append(f.sends, req)
	return &esign.SendResp{EnvelopeID: "env-" + req.RequestID, Status: "sent"}, nil
}
func (f *fakeESignClient) GetStatus(context.Context, esign.StatusReq) (*esign.StatusResp, error) {
	return nil, errors.New("not used")
}
func (f *fakeESignClient) GetSignedDocument(context.Context, esign.GetSignedReq) (*esign.GetSignedResp, error) {
	return nil, errors.New("not used")
}
func (f *fakeESignClient) ParseWebhook(http.Header, []byte) (*esign.WebhookEvent, error) {
	return nil, errors.New("not used")
}

const storedPDF = "%PDF-1.7 the stored version bytes"

// wireESign plugs a fake vendor client and a fake document fetcher that
// serves storedPDF for the fixture's (document, version) only, recording the
// actor it was asked to read as.
func (f *svcFixture) wireESign(t *testing.T) (*fakeESignClient, *[]Actor) {
	t.Helper()
	client := &fakeESignClient{}
	var actors []Actor
	f.svc.esignClientFn = func(context.Context, string, esign.Provider) (esign.ESignClient, error) {
		return client, nil
	}
	f.svc.versionBytesFn = func(_ context.Context, a Actor, documentID, versionID string) ([]byte, error) {
		actors = append(actors, a)
		if documentID != f.doc.String() || versionID != f.ver.String() {
			return nil, errors.New("unexpected version")
		}
		return []byte(storedPDF), nil
	}
	return client, &actors
}

func (f *svcFixture) ownerActor() Actor {
	return Actor{TenantID: f.tenant.ID.String(), UserID: f.tenant.Owner.String(), Role: "owner", Email: f.tenant.OwnerEmail}
}

func TestEsignSend_NonCreatorForbidden(t *testing.T) {
	f := newSvcFixture(t)
	client, _ := f.wireESign(t)
	req := f.createRequest(t, "docusign", model.Signer{Email: "a@test.local", Name: "A", Role: "signer", Order: 1})

	_, err := f.svc.SendViaProvider(context.Background(), &SendESignInput{
		Actor:     Actor{TenantID: f.tenant.ID.String(), UserID: f.tenant.Member.String(), Role: "member", Email: f.tenant.MemberEmail},
		RequestID: req.ID, Provider: esign.ProviderDocuSign,
	})
	require.True(t, errors.Is(err, ErrForbidden), "a member who did not create the request must not send it, got %v", err)
	require.Empty(t, client.sends, "no envelope may be created")
	require.Equal(t, "pending", f.status(t, req.ID))
}

func TestEsignSend_CompletedRequestRejected(t *testing.T) {
	f := newSvcFixture(t)
	client, _ := f.wireESign(t)
	req := f.createRequest(t, "docusign", model.Signer{Email: "a@test.local", Name: "A", Role: "signer", Order: 1})
	_, err := f.db.Super.Exec(context.Background(),
		`UPDATE signature_requests SET status = 'completed', completed_at = now() WHERE tenant_id = $1 AND id = $2`,
		f.tenant.ID, req.ID)
	require.NoError(t, err)

	_, err = f.svc.SendViaProvider(context.Background(), &SendESignInput{
		Actor: f.ownerActor(), RequestID: req.ID, Provider: esign.ProviderDocuSign,
	})
	require.True(t, errors.Is(err, ErrRequestNotSignable), "a completed request must not be (re)sent, got %v", err)
	require.Empty(t, client.sends)
	require.Equal(t, "completed", f.status(t, req.ID))
}

func TestEsignSend_UsesStoredVersionBytes(t *testing.T) {
	f := newSvcFixture(t)
	client, actors := f.wireESign(t)
	req := f.createRequest(t, "docusign",
		model.Signer{Email: "a@test.local", Name: "A", Role: "signer", Order: 1},
		model.Signer{Email: "b@test.local", Name: "B", Role: "signer", Order: 2})

	out, err := f.svc.SendViaProvider(context.Background(), &SendESignInput{
		Actor: f.ownerActor(), RequestID: req.ID, Provider: esign.ProviderDocuSign,
		DocumentName: "contract.pdf", EmbeddedSignerIDs: []string{req.Signers[1].ID},
	})
	require.NoError(t, err)
	require.Equal(t, "env-"+req.ID, out.EnvelopeID)

	require.Len(t, client.sends, 1)
	sent := client.sends[0]
	require.Equal(t, storedPDF, string(sent.DocumentBytes), "the vendor must receive the stored version's bytes")
	require.Len(t, sent.Recipients, 2, "recipients come from the request's signer rows")
	require.Equal(t, "a@test.local", sent.Recipients[0].Email)
	require.False(t, sent.Recipients[0].Embedded)
	require.Equal(t, "b@test.local", sent.Recipients[1].Email)
	require.True(t, sent.Recipients[1].Embedded)

	require.Len(t, *actors, 1)
	require.Equal(t, f.tenant.Owner.String(), (*actors)[0].UserID, "bytes are read as the caller, so document permissions apply")

	require.Equal(t, "in_progress", f.status(t, req.ID))
	_, err = f.svc.SendViaProvider(context.Background(), &SendESignInput{
		Actor: f.ownerActor(), RequestID: req.ID, Provider: esign.ProviderDocuSign,
	})
	require.True(t, errors.Is(err, ErrRequestNotSignable), "an already-sent request must not be rebound to a second envelope, got %v", err)
	require.Len(t, client.sends, 1)
}
