//go:build integration

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

// Adobe Acrobat Sign webhooks carry no body MAC; the only documented check
// is the X-AdobeSign-ClientId header, and a client id is not a secret. A
// "completed" notification is therefore only a hint: the agreement must be
// confirmed SIGNED through the agreements API (with the stored OAuth token)
// before the signed PDF is pulled and a version is created. Until it is,
// the webhook is answered with a retryable error and nothing is written.
func TestAdobeWebhook_CompletionConfirmedBeforeIngest(t *testing.T) {
	db := repotest.New(t)
	ctx := context.Background()
	tn := repotest.SeedTenant(ctx, t, db.Super)
	doc, ver := repotest.SeedDocument(ctx, t, db.Super, tn.ID, tn.Owner)

	var mu sync.Mutex
	vendorStatus := "OUT_FOR_SIGNATURE"
	var fetches, statusCalls int32
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer at", r.Header.Get("Authorization"))
		switch {
		case strings.HasSuffix(r.URL.Path, "/combinedDocument"), strings.HasSuffix(r.URL.Path, "/auditTrail"):
			atomic.AddInt32(&fetches, 1)
			_, _ = w.Write([]byte("%PDF signed by vendor"))
		case r.URL.Path == "/api/rest/v6/agreements/AGR-1":
			atomic.AddInt32(&statusCalls, 1)
			mu.Lock()
			st := vendorStatus
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "AGR-1", "status": st})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(vendor.Close)

	repo := repository.New(db.App)
	svc := service.New(service.Config{Pool: db.App, Repo: repo, Outbox: database.NewOutboxRepository(), Logger: zerolog.Nop()})
	sealKey := []byte("0123456789abcdef0123456789abcdef")
	svc.AddESign(service.ESignConfig{SealingKey: sealKey, DefaultHMACSecret: sealKey, HTTPClient: vendor.Client()})
	ingest := &countingIngest{}
	svc.AddIngest(ingest)
	require.NoError(t, svc.SaveProviderConfig(ctx, tn.ID.String(), tn.Owner.String(),
		esign.ProviderAdobeSign, "adobe-client-id", "adobe-client-secret", "sandbox", "", "", ""))
	sealedAccess, err := esign.SealString([]byte("at"), sealKey)
	require.NoError(t, err)
	require.NoError(t, repo.UpsertESignToken(ctx, &repository.ESignToken{
		TenantID: tn.ID.String(), Provider: string(esign.ProviderAdobeSign), AccessToken: sealedAccess,
		ExpiresAt: time.Now().Add(24 * time.Hour), AccountID: "acc", BaseURI: vendor.URL,
		ConnectedBy: tn.Owner.String(), ConnectedAt: time.Now(), UpdatedAt: time.Now(),
	}))
	mux := http.NewServeMux()
	New(svc, zerolog.Nop()).RegisterESignWebhook(mux)

	req, err := svc.CreateRequest(ctx, tn.ID.String(), doc.String(), ver.String(), tn.Owner.String(),
		string(esign.ProviderAdobeSign), "remote", []model.Signer{{Email: "a@test.local", Name: "A", Role: "signer", Order: 1}})
	require.NoError(t, err)
	require.NoError(t, database.WithTenantTx(ctx, db.App, tn.ID, func(tx pgx.Tx) error {
		return repo.SetEnvelopeIDTx(ctx, tx, tn.ID.String(), req.ID, "AGR-1")
	}))
	status := func() string {
		var st string
		require.NoError(t, db.Super.QueryRow(ctx, `SELECT status FROM signature_requests WHERE tenant_id = $1 AND id = $2`, tn.ID, req.ID).Scan(&st))
		return st
	}
	deliver := func(notification string) *httptest.ResponseRecorder {
		body := []byte(fmt.Sprintf(`{"webhookNotificationId":%q,"event":"AGREEMENT_WORKFLOW_COMPLETED",`+
			`"eventDate":"2026-10-12T10:00:00Z","agreement":{"id":"AGR-1","status":"SIGNED"}}`, notification))
		r := httptest.NewRequest(http.MethodPost, "/api/v1/signatures/esign/webhook/adobe_sign/"+tn.ID.String(), bytes.NewReader(body))
		r.Header.Set("X-AdobeSign-ClientId", "adobe-client-id")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}

	// The notification claims SIGNED but the agreement is still out for
	// signature: nothing is pulled or written, and the vendor is asked to
	// retry.
	w := deliver("n-1")
	require.GreaterOrEqual(t, w.Code, 500, "an unconfirmed completion must be retried: %d %s", w.Code, w.Body.String())
	require.EqualValues(t, 1, atomic.LoadInt32(&statusCalls), "the agreement status must be confirmed with the vendor")
	require.Zero(t, atomic.LoadInt32(&fetches), "the signed PDF must not be pulled before confirmation")
	require.Zero(t, atomic.LoadInt32(&ingest.versions), "no version before confirmation")
	require.NotEqual(t, "completed", status())

	// The vendor confirms: the redelivery ingests exactly one version.
	mu.Lock()
	vendorStatus = "SIGNED"
	mu.Unlock()
	w = deliver("n-1")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.EqualValues(t, 1, atomic.LoadInt32(&ingest.versions))
	require.Equal(t, "completed", status())
}
