//go:build integration

// S-3 / S-10 / DEC-17: QES start binds the ceremony to the calling signer
// and to the request's stored version bytes; QES return persists the TSP's
// signed hash; concurrent returns cannot flip a completed session to failed.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	tsplib "github.com/aieera/sedoc/pkg/signing/tsp"
	"github.com/aieera/sedoc/services/signature/internal/model"
)

// The DB CHECK on tsp_signing_sessions.provider admits only the real QTSP
// names, so the mock adapter is registered under the swisscom slot.
func (f *svcFixture) wireQES(t *testing.T, client tsplib.TSPClient) {
	t.Helper()
	f.svc.AddQES(QESConfig{
		Clients:       map[tsplib.Provider]tsplib.TSPClient{tsplib.ProviderSwisscom: client},
		PublicBaseURL: "https://app.test",
		SessionTTL:    15 * time.Minute,
	})
	f.wireESign(t) // installs the stored-version fetcher
}

func (f *svcFixture) memberActor() Actor {
	return Actor{TenantID: f.tenant.ID.String(), UserID: f.tenant.Member.String(), Role: "member", Email: f.tenant.MemberEmail}
}

// qesRequest creates a request whose only signer is the tenant member.
func (f *svcFixture) qesRequest(t *testing.T) *model.SignatureRequest {
	t.Helper()
	return f.createRequest(t, "", model.Signer{Email: f.tenant.MemberEmail, Name: "Member", Role: "signer", Order: 1})
}

// returnParams extracts (session, state, code) from the mock QTSP redirect.
func returnParams(t *testing.T, redirect string) HandleReturnInput {
	t.Helper()
	u, err := url.Parse(redirect)
	require.NoError(t, err)
	q := u.Query()
	return HandleReturnInput{SessionID: q.Get("session"), State: q.Get("state"), AuthCode: q.Get("code")}
}

func (f *svcFixture) sessionStatus(t *testing.T, id string) string {
	t.Helper()
	var st string
	require.NoError(t, f.db.Super.QueryRow(context.Background(),
		`SELECT status FROM tsp_signing_sessions WHERE tenant_id = $1 AND id = $2`, f.tenant.ID, id).Scan(&st))
	return st
}

func TestQESStart_SignerMustBeCaller(t *testing.T) {
	f := newSvcFixture(t)
	f.wireQES(t, tsplib.NewMock())
	req := f.qesRequest(t)
	ctx := context.Background()

	_, err := f.svc.StartQES(ctx, f.ownerActor(), &StartQESInput{
		RequestID: req.ID, SignerID: req.Signers[0].ID, Provider: tsplib.ProviderSwisscom,
	})
	require.True(t, errors.Is(err, ErrForbidden), "only the signer may start their QES ceremony, got %v", err)

	out, err := f.svc.StartQES(ctx, f.memberActor(), &StartQESInput{
		RequestID: req.ID, SignerID: req.Signers[0].ID, Provider: tsplib.ProviderSwisscom,
	})
	require.NoError(t, err)
	sess, err := f.svc.GetSession(ctx, f.tenant.ID.String(), out.SessionID)
	require.NoError(t, err)
	sum := sha256.Sum256([]byte(storedPDF))
	require.Equal(t, hex.EncodeToString(sum[:]), sess.DocumentHash, "the hash sent to the QTSP is of the stored version")

	require.NoError(t, f.svc.CancelRequest(ctx, f.tenant.ID.String(), req.ID, f.tenant.Owner.String(), false))
	_, err = f.svc.StartQES(ctx, f.memberActor(), &StartQESInput{
		RequestID: req.ID, SignerID: req.Signers[0].ID, Provider: tsplib.ProviderSwisscom,
	})
	require.True(t, errors.Is(err, ErrRequestNotSignable), "a cancelled request cannot start QES, got %v", err)
}

func TestQESReturn_PersistsSignedHash(t *testing.T) {
	f := newSvcFixture(t)
	f.wireQES(t, tsplib.NewMock())
	req := f.qesRequest(t)
	ctx := context.Background()

	out, err := f.svc.StartQES(ctx, f.memberActor(), &StartQESInput{
		RequestID: req.ID, SignerID: req.Signers[0].ID, Provider: tsplib.ProviderSwisscom,
	})
	require.NoError(t, err)
	res, err := f.svc.HandleReturn(ctx, f.tenant.ID.String(), returnParams(t, out.RedirectURL))
	require.NoError(t, err)
	require.Equal(t, "completed", res.Status)

	var raw []byte
	var certID *string
	require.NoError(t, f.db.Super.QueryRow(ctx,
		`SELECT signature_data, certificate_id FROM signature_signers WHERE tenant_id = $1 AND id = $2`,
		f.tenant.ID, req.Signers[0].ID).Scan(&raw, &certID))
	require.NotNil(t, raw, "the TSP signed hash must be persisted, not discarded")
	var data map[string]string
	require.NoError(t, json.Unmarshal(raw, &data))
	signed, err := base64.StdEncoding.DecodeString(data["signed_hash_b64"])
	require.NoError(t, err)
	require.Len(t, signed, sha256.Size)
	require.Equal(t, out.SessionID, data["session_id"])
	require.NotNil(t, certID)
	require.NotEmpty(t, *certID)
}

// raceTSP makes two returns reach Sign together: the first waits for the
// second to arrive, the second waits for the first to persist completion
// and then fails as "consumed" — the interleaving that used to flip a
// completed session to failed.
type raceTSP struct {
	*tsplib.MockClient
	mu        sync.Mutex
	calls     int
	arrived   chan struct{}
	completed func() bool
}

func (r *raceTSP) Sign(ctx context.Context, req tsplib.SignReq) (*tsplib.SignResp, error) {
	r.mu.Lock()
	r.calls++
	n := r.calls
	r.mu.Unlock()
	if n == 1 {
		select {
		case <-r.arrived:
		case <-time.After(2 * time.Second):
		}
		return r.MockClient.Sign(ctx, req)
	}
	close(r.arrived)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !r.completed() {
		time.Sleep(20 * time.Millisecond)
	}
	return nil, tsplib.ErrAlreadyConsumed
}

func TestQESReturn_ConcurrentReturnKeepsCompleted(t *testing.T) {
	f := newSvcFixture(t)
	var sessionID string
	tsp := &raceTSP{MockClient: tsplib.NewMock(), arrived: make(chan struct{})}
	tsp.completed = func() bool { return sessionID != "" && f.sessionStatus(t, sessionID) == "completed" }
	f.wireQES(t, tsp)
	req := f.qesRequest(t)
	ctx := context.Background()

	out, err := f.svc.StartQES(ctx, f.memberActor(), &StartQESInput{
		RequestID: req.ID, SignerID: req.Signers[0].ID, Provider: tsplib.ProviderSwisscom,
	})
	require.NoError(t, err)
	sessionID = out.SessionID
	in := returnParams(t, out.RedirectURL)

	var wg sync.WaitGroup
	results := make([]*HandleReturnResult, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := f.svc.HandleReturn(ctx, f.tenant.ID.String(), in)
			if err == nil {
				results[i] = res
			}
		}(i)
	}
	wg.Wait()

	require.Equal(t, "completed", f.sessionStatus(t, sessionID), "a completed QES session must stay completed")
	for _, r := range results {
		require.NotNil(t, r)
		require.NotEqual(t, "failed", r.Status, "the losing return must not report (or record) a failure")
	}
}
