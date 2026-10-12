//go:build integration

package handler

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/esign"
	"github.com/aieera/sedoc/services/signature/internal/repository"
	"github.com/aieera/sedoc/services/signature/internal/repository/repotest"
	"github.com/aieera/sedoc/services/signature/internal/service"
)

func docuSignSig(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// S-9: the webhook must verify the HMAC with the tenant's stored secret
// before resolving any vendor client, and every unauthenticated outcome —
// connected tenant with a bad signature, unconnected tenant, unknown or
// malformed tenant id — must be the same 401 with the same body, so the
// endpoint cannot be used to enumerate tenants or their integrations.
func TestEsignWebhook_UnconnectedTenantIndistinguishable(t *testing.T) {
	db := repotest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	connected := repotest.SeedTenant(ctx, t, db.Super)
	unconnected := repotest.SeedTenant(ctx, t, db.Super)

	repo := repository.New(db.App)
	svc := service.New(service.Config{
		Pool: db.App, Repo: repo, Outbox: database.NewOutboxRepository(), Logger: zerolog.Nop(),
	})
	sealKey := []byte("0123456789abcdef0123456789abcdef")
	svc.AddESign(service.ESignConfig{SealingKey: sealKey, DefaultHMACSecret: sealKey})

	const secret = "tenant-a-client-secret"
	require.NoError(t, svc.SaveProviderConfig(ctx, connected.ID.String(), connected.Owner.String(),
		esign.ProviderDocuSign, "cid", secret, "sandbox", "", "", ""))
	sealedAccess, err := esign.SealString([]byte("at"), sealKey)
	require.NoError(t, err)
	require.NoError(t, repo.UpsertESignToken(ctx, &repository.ESignToken{
		TenantID: connected.ID.String(), Provider: "docusign", AccessToken: sealedAccess,
		ExpiresAt: time.Now().Add(24 * time.Hour), AccountID: "acc", BaseURI: "https://demo.docusign.net",
		ConnectedBy: connected.Owner.String(), ConnectedAt: time.Now(), UpdatedAt: time.Now(),
	}))

	mux := http.NewServeMux()
	New(svc, zerolog.Nop()).RegisterESignWebhook(mux)
	post := func(tenant string, body []byte, sig string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/signatures/esign/webhook/docusign/"+tenant, bytes.NewReader(body))
		if sig != "" {
			r.Header.Set("X-DocuSign-Signature-1", sig)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}

	body := []byte(`{"event":"envelope-completed","envelopeId":"unknown-env","status":"completed"}`)
	forged := docuSignSig("attacker-guess", body)

	ref := post(connected.ID.String(), body, forged)
	require.Equal(t, http.StatusUnauthorized, ref.Code, ref.Body.String())
	for name, w := range map[string]*httptest.ResponseRecorder{
		"connected, no signature": post(connected.ID.String(), body, ""),
		"unconnected tenant":      post(unconnected.ID.String(), body, forged),
		"unknown tenant":          post(uuid.NewString(), body, forged),
		"malformed tenant":        post("not-a-uuid", body, forged),
	} {
		require.Equal(t, ref.Code, w.Code, "%s: status differs from a bad signature on a connected tenant", name)
		require.Equal(t, ref.Body.String(), w.Body.String(), "%s: body differs from a bad signature on a connected tenant", name)
	}

	// A correctly signed event for the connected tenant is accepted (an
	// unknown envelope is ignored) — verification uses the stored secret.
	ok := post(connected.ID.String(), body, docuSignSig(secret, body))
	require.Equal(t, http.StatusOK, ok.Code, ok.Body.String())
}
