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
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/signature/internal/model"
	"github.com/aieera/sedoc/services/signature/internal/repository"
	"github.com/aieera/sedoc/services/signature/internal/repository/repotest"
	"github.com/aieera/sedoc/services/signature/internal/service"
)

type apiFixture struct {
	db     *testutil.ProdPostureDB
	svc    *service.Service
	mux    *http.ServeMux
	tenant repotest.Tenant
	doc    uuid.UUID
	ver    uuid.UUID
}

func newAPIFixture(t *testing.T) *apiFixture {
	t.Helper()
	db := repotest.New(t)
	ctx := context.Background()
	tn := repotest.SeedTenant(ctx, t, db.Super)
	doc, ver := repotest.SeedDocument(ctx, t, db.Super, tn.ID, tn.Owner)
	svc := service.New(service.Config{
		Pool: db.App, Repo: repository.New(db.App),
		Outbox: database.NewOutboxRepository(), Logger: zerolog.Nop(),
	})
	h := New(svc, zerolog.Nop())
	mux := http.NewServeMux()
	h.Register(mux)
	h.RegisterInPerson(mux)
	return &apiFixture{db: db, svc: svc, mux: mux, tenant: tn, doc: doc, ver: ver}
}

// as runs one request as the given authenticated user.
func (f *apiFixture) as(t *testing.T, userID uuid.UUID, email, role, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(auth.WithUser(r.Context(), auth.UserInfo{
		ID: userID, TenantID: f.tenant.ID, Email: email, Role: role,
	}))
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}

// H-1: the per-signer signing token is the only proof a caller is the
// signer, so it must never be readable by other tenant members. It is
// returned once, in the create response to the request's creator.
func TestGetRequest_NoSigningTokens(t *testing.T) {
	f := newAPIFixture(t)

	createBody := fmt.Sprintf(`{"document_id":%q,"version_id":%q,"signers":[
		{"email":"a@test.local","name":"A","role":"signer","order":1,"signing_url":"/sign/x/y?token=client-chosen"},
		{"email":"b@test.local","name":"B","role":"signer","order":2}]}`, f.doc, f.ver)
	w := f.as(t, f.tenant.Owner, f.tenant.OwnerEmail, "owner", http.MethodPost, "/api/v1/signatures/requests", createBody)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var created model.SignatureRequest
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	require.Len(t, created.Signers, 2)
	var tokens []string
	for _, s := range created.Signers {
		require.NotEmpty(t, s.SigningURL, "the creator's 201 carries each signer's signing URL")
		require.NotContains(t, s.SigningURL, "client-chosen", "the server mints signing tokens; a client value must be ignored")
		_, tok, _ := strings.Cut(s.SigningURL, "token=")
		require.NotEmpty(t, tok)
		tokens = append(tokens, tok)
	}

	for _, path := range []string{
		"/api/v1/signatures/requests/" + created.ID,
		"/api/v1/signatures/document/" + f.doc.String(),
	} {
		w := f.as(t, f.tenant.Member, f.tenant.MemberEmail, "member", http.MethodGet, path, "")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		body := w.Body.String()
		require.NotContains(t, body, "signing_url", "GET %s must not expose signing URLs", path)
		for _, tok := range tokens {
			require.NotContains(t, body, tok, "GET %s leaked a signing token", path)
		}
	}
}
