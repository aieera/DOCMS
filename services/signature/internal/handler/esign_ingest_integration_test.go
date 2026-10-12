//go:build integration

package handler

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/esign"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/signature/internal/model"
	"github.com/aieera/sedoc/services/signature/internal/repository"
	"github.com/aieera/sedoc/services/signature/internal/repository/repotest"
	"github.com/aieera/sedoc/services/signature/internal/service"
)

// countingIngest records every attempt to upload signed bytes or mint a
// version from them.
type countingIngest struct{ puts, versions int32 }

func (c *countingIngest) PutSignedBlob(_ context.Context, in service.PutSignedBlobInput) (*service.PutSignedBlobResult, error) {
	atomic.AddInt32(&c.puts, 1)
	return &service.PutSignedBlobResult{ContentBlobID: "blob-1", SHA256: "x", SizeBytes: int64(len(in.Bytes))}, nil
}

func (c *countingIngest) CreateVersionFromBlob(context.Context, service.CreateVersionFromBlobInput) (string, error) {
	atomic.AddInt32(&c.versions, 1)
	return "version-1", nil
}

const ingestTestSecret = "tenant-client-secret"

// ingestFixture is a tenant connected to a fake DocuSign (real adapter,
// httptest vendor) with a counting ingest client and the webhook route.
type ingestFixture struct {
	db            *testutil.ProdPostureDB
	tn            repotest.Tenant
	doc, ver      string
	repo          *repository.Repository
	svc           *service.Service
	ingest        *countingIngest
	vendorFetches *int32
	mux           *http.ServeMux
}

func newIngestFixture(t *testing.T) *ingestFixture {
	t.Helper()
	db := repotest.New(t)
	ctx := context.Background()
	tn := repotest.SeedTenant(ctx, t, db.Super)
	doc, ver := repotest.SeedDocument(ctx, t, db.Super, tn.ID, tn.Owner)

	var fetches int32
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/documents/combined") {
			atomic.AddInt32(&fetches, 1)
			_, _ = w.Write([]byte("%PDF signed by vendor"))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(vendor.Close)

	repo := repository.New(db.App)
	svc := service.New(service.Config{
		Pool: db.App, Repo: repo, Outbox: database.NewOutboxRepository(), Logger: zerolog.Nop(),
	})
	sealKey := []byte("0123456789abcdef0123456789abcdef")
	svc.AddESign(service.ESignConfig{SealingKey: sealKey, DefaultHMACSecret: sealKey, HTTPClient: vendor.Client()})
	ingest := &countingIngest{}
	svc.AddIngest(ingest)

	require.NoError(t, svc.SaveProviderConfig(ctx, tn.ID.String(), tn.Owner.String(),
		esign.ProviderDocuSign, "cid", ingestTestSecret, "sandbox", "", "", ""))
	sealedAccess, err := esign.SealString([]byte("at"), sealKey)
	require.NoError(t, err)
	require.NoError(t, repo.UpsertESignToken(ctx, &repository.ESignToken{
		TenantID: tn.ID.String(), Provider: "docusign", AccessToken: sealedAccess,
		ExpiresAt: time.Now().Add(24 * time.Hour), AccountID: "acc", BaseURI: vendor.URL,
		ConnectedBy: tn.Owner.String(), ConnectedAt: time.Now(), UpdatedAt: time.Now(),
	}))

	mux := http.NewServeMux()
	New(svc, zerolog.Nop()).RegisterESignWebhook(mux)
	return &ingestFixture{
		db: db, tn: tn, doc: doc.String(), ver: ver.String(), repo: repo, svc: svc,
		ingest: ingest, vendorFetches: &fetches, mux: mux,
	}
}

// sentRequest creates a DocuSign request and stamps envelopeID on it as a
// send would (status → in_progress).
func (f *ingestFixture) sentRequest(t *testing.T, envelopeID string) string {
	t.Helper()
	ctx := context.Background()
	req, err := f.svc.CreateRequest(ctx, f.tn.ID.String(), f.doc, f.ver, f.tn.Owner.String(),
		"docusign", "remote", []model.Signer{{Email: "a@test.local", Name: "A", Role: "signer", Order: 1}})
	require.NoError(t, err)
	require.NoError(t, database.WithTenantTx(ctx, f.db.App, f.tn.ID, func(tx pgx.Tx) error {
		return f.repo.SetEnvelopeIDTx(ctx, tx, f.tn.ID.String(), req.ID, envelopeID)
	}))
	return req.ID
}

// deliverCompleted posts a correctly signed DocuSign "completed" event.
func (f *ingestFixture) deliverCompleted(envelopeID string) *httptest.ResponseRecorder {
	body := []byte(fmt.Sprintf(`{"event":"envelope-completed","envelopeId":%q,"generatedDateTime":"2026-10-12T10:00:00Z","status":"completed"}`, envelopeID))
	r := httptest.NewRequest(http.MethodPost, "/api/v1/signatures/esign/webhook/docusign/"+f.tn.ID.String(), bytes.NewReader(body))
	r.Header.Set("X-DocuSign-Signature-1", docuSignSig(ingestTestSecret, body))
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}

func (f *ingestFixture) status(t *testing.T, requestID string) string {
	t.Helper()
	var st string
	require.NoError(t, f.db.Super.QueryRow(context.Background(),
		`SELECT status FROM signature_requests WHERE tenant_id = $1 AND id = $2`, f.tn.ID, requestID).Scan(&st))
	return st
}

// A vendor "completed" webhook for a request that was cancelled after it was
// sent must not pull the signed PDF or mint a new version, and must be
// acknowledged (200) so the vendor stops retrying — previously the version
// was written first and the now-conditional completion then failed with a
// 500, so every vendor retry ingested another version.
func TestEsignIngest_CancelledRequestNoNewVersion(t *testing.T) {
	f := newIngestFixture(t)
	reqID := f.sentRequest(t, "env-1")
	require.NoError(t, f.svc.CancelRequest(context.Background(), f.tn.ID.String(), reqID, f.tn.Owner.String(), false))

	w := f.deliverCompleted("env-1")
	require.Zero(t, atomic.LoadInt32(&f.ingest.versions), "no new version may be created")
	require.Zero(t, atomic.LoadInt32(&f.ingest.puts), "no signed blob may be uploaded")
	require.Zero(t, atomic.LoadInt32(f.vendorFetches), "the signed PDF must not be pulled for a cancelled request")
	require.Equal(t, http.StatusOK, w.Code, "a completed webhook for a cancelled request must be acknowledged: %s", w.Body.String())
	require.Equal(t, "cancelled", f.status(t, reqID))

	// Control: the same delivery for an open, sent request still ingests the
	// signed PDF as a new version and completes the request.
	openID := f.sentRequest(t, "env-2")
	w = f.deliverCompleted("env-2")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.EqualValues(t, 1, atomic.LoadInt32(&f.ingest.versions), "an open request's completed envelope becomes a new version")
	require.Equal(t, "completed", f.status(t, openID))
}

// The vendor owns envelope expiry: SeDoc's expires_at (now+14d) is never sent
// to the envelope and nothing moves a vendor request to `expired`. An
// envelope the vendor completes after that date must still be ingested and
// complete the request — otherwise it is acked and dropped, the request stays
// in_progress forever and the reconciler polls the vendor indefinitely.
func TestEsignIngest_ExpiredButOpenRequestStillIngests(t *testing.T) {
	f := newIngestFixture(t)
	reqID := f.sentRequest(t, "env-late")
	_, err := f.db.Super.Exec(context.Background(),
		`UPDATE signature_requests SET expires_at = now() - interval '1 day' WHERE tenant_id = $1 AND id = $2`,
		f.tn.ID, reqID)
	require.NoError(t, err)
	require.Equal(t, "in_progress", f.status(t, reqID))

	w := f.deliverCompleted("env-late")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.EqualValues(t, 1, atomic.LoadInt32(&f.ingest.versions), "a late-completed envelope still becomes exactly one new version")
	require.EqualValues(t, 1, atomic.LoadInt32(f.vendorFetches))
	require.Equal(t, "completed", f.status(t, reqID))
}
