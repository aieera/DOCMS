//go:build integration

package handler

import (
	"bytes"
	"context"
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

// A vendor "completed" webhook for a request that was cancelled after it was
// sent must not pull the signed PDF or mint a new version, and must be
// acknowledged (200) so the vendor stops retrying — previously the version
// was written first and the now-conditional completion then failed with a
// 500, so every vendor retry ingested another version.
func TestEsignIngest_CancelledRequestNoNewVersion(t *testing.T) {
	db := repotest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	tn := repotest.SeedTenant(ctx, t, db.Super)
	doc, ver := repotest.SeedDocument(ctx, t, db.Super, tn.ID, tn.Owner)

	var vendorFetches int32
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/documents/combined") {
			atomic.AddInt32(&vendorFetches, 1)
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

	const secret = "tenant-client-secret"
	require.NoError(t, svc.SaveProviderConfig(ctx, tn.ID.String(), tn.Owner.String(),
		esign.ProviderDocuSign, "cid", secret, "sandbox", "", "", ""))
	sealedAccess, err := esign.SealString([]byte("at"), sealKey)
	require.NoError(t, err)
	require.NoError(t, repo.UpsertESignToken(ctx, &repository.ESignToken{
		TenantID: tn.ID.String(), Provider: "docusign", AccessToken: sealedAccess,
		ExpiresAt: time.Now().Add(24 * time.Hour), AccountID: "acc", BaseURI: vendor.URL,
		ConnectedBy: tn.Owner.String(), ConnectedAt: time.Now(), UpdatedAt: time.Now(),
	}))

	// A sent request (envelope stamped, in_progress) that is then cancelled.
	req, err := svc.CreateRequest(ctx, tn.ID.String(), doc.String(), ver.String(), tn.Owner.String(),
		"docusign", "remote", []model.Signer{{Email: "a@test.local", Name: "A", Role: "signer", Order: 1}})
	require.NoError(t, err)
	require.NoError(t, database.WithTenantTx(ctx, db.App, tn.ID, func(tx pgx.Tx) error {
		return repo.SetEnvelopeIDTx(ctx, tx, tn.ID.String(), req.ID, "env-1")
	}))
	require.NoError(t, svc.CancelRequest(ctx, tn.ID.String(), req.ID, tn.Owner.String(), false))

	mux := http.NewServeMux()
	New(svc, zerolog.Nop()).RegisterESignWebhook(mux)
	body := []byte(`{"event":"envelope-completed","envelopeId":"env-1","generatedDateTime":"2026-10-12T10:00:00Z","status":"completed"}`)
	deliver := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/signatures/esign/webhook/docusign/"+tn.ID.String(), bytes.NewReader(body))
		r.Header.Set("X-DocuSign-Signature-1", docuSignSig(secret, body))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}

	w := deliver()
	require.Zero(t, atomic.LoadInt32(&ingest.versions), "no new version may be created")
	require.Zero(t, atomic.LoadInt32(&ingest.puts), "no signed blob may be uploaded")
	require.Zero(t, atomic.LoadInt32(&vendorFetches), "the signed PDF must not be pulled for a cancelled request")
	require.Equal(t, http.StatusOK, w.Code, "a completed webhook for a cancelled request must be acknowledged: %s", w.Body.String())

	var status string
	require.NoError(t, db.Super.QueryRow(ctx,
		`SELECT status FROM signature_requests WHERE tenant_id = $1 AND id = $2`, tn.ID, req.ID).Scan(&status))
	require.Equal(t, "cancelled", status)

	// Control: the same delivery for an open, sent request still ingests the
	// signed PDF as a new version and completes the request.
	open, err := svc.CreateRequest(ctx, tn.ID.String(), doc.String(), ver.String(), tn.Owner.String(),
		"docusign", "remote", []model.Signer{{Email: "b@test.local", Name: "B", Role: "signer", Order: 1}})
	require.NoError(t, err)
	require.NoError(t, database.WithTenantTx(ctx, db.App, tn.ID, func(tx pgx.Tx) error {
		return repo.SetEnvelopeIDTx(ctx, tx, tn.ID.String(), open.ID, "env-2")
	}))
	body = []byte(`{"event":"envelope-completed","envelopeId":"env-2","generatedDateTime":"2026-10-12T10:05:00Z","status":"completed"}`)
	w = deliver()
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.EqualValues(t, 1, atomic.LoadInt32(&ingest.versions), "an open request's completed envelope becomes a new version")
	require.NoError(t, db.Super.QueryRow(ctx,
		`SELECT status FROM signature_requests WHERE tenant_id = $1 AND id = $2`, tn.ID, open.ID).Scan(&status))
	require.Equal(t, "completed", status)
}
