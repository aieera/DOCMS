//go:build integration
// +build integration

// D-17 (document half) — re-applying WORM must never shorten a document's
// recorded retention, and a storage refusal must reach the caller as 409.
//
// worm-lock overwrote documents.worm_retain_until with whatever date the
// request carried, so a second lock with an earlier date shortened the
// window the write-path guard (blockedByWORM) enforces. The effective date
// is now GREATEST(existing, requested) — it is what storage is asked to
// apply and what the document records — and storage's 409 ("existing lock
// cannot be shortened or downgraded") is surfaced as 409, not 500.
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
)

const wormFixtureDDL = `
CREATE TABLE documents (
	tenant_id         UUID NOT NULL,
	id                UUID NOT NULL,
	worm_retain_until TIMESTAMPTZ,
	updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (tenant_id, id)
);
CREATE TABLE document_versions (
	tenant_id       UUID NOT NULL,
	id              UUID NOT NULL DEFAULT gen_random_uuid(),
	document_id     UUID NOT NULL,
	version_number  INT  NOT NULL,
	content_blob_id UUID NOT NULL,
	PRIMARY KEY (tenant_id, id)
);
`

type fakeStorageWORM struct {
	mu       sync.Mutex
	status   int
	received []string // retain_until values storage was asked to apply
}

func (f *fakeStorageWORM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RetainUntil string `json:"retain_until"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.received = append(f.received, body.RetainUntil)
	if f.status != 0 && f.status != http.StatusOK {
		http.Error(w, "CONFLICT: existing WORM retention cannot be shortened or downgraded", f.status)
		return
	}
	_, _ = w.Write([]byte(`{}`))
}

type wormHarness struct {
	ctx     context.Context
	pool    *pgxpool.Pool
	mux     *http.ServeMux
	storage *fakeStorageWORM
	tenant  uuid.UUID
	doc     uuid.UUID
}

func newWORMHarness(t *testing.T) *wormHarness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	dsn, clean, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(clean)
	cfg := database.DefaultPoolConfig()
	cfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, wormFixtureDDL)
	require.NoError(t, err)

	tenant, doc := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	_, err = pool.Exec(ctx, `INSERT INTO documents (tenant_id, id) VALUES ($1, $2)`, tenant, doc)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO document_versions (tenant_id, document_id, version_number, content_blob_id)
		VALUES ($1, $2, 1, $3)`, tenant, doc, uuid.Must(uuid.NewV7()))
	require.NoError(t, err)

	fs := &fakeStorageWORM{}
	srv := httptest.NewServer(fs)
	t.Cleanup(srv.Close)
	h := &WORMHandler{pool: pool, log: zerolog.Nop(), storageURL: srv.URL, internKey: "k", hc: srv.Client()}
	mux := http.NewServeMux()
	h.Register(mux)
	return &wormHarness{ctx: ctx, pool: pool, mux: mux, storage: fs, tenant: tenant, doc: doc}
}

func (h *wormHarness) lock(t *testing.T, until time.Time, mode string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"retain_until": until.Format(time.RFC3339), "mode": mode})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/documents/"+h.doc.String()+"/worm-lock", bytes.NewReader(body))
	req = req.WithContext(auth.WithUser(req.Context(), auth.UserInfo{
		ID: uuid.Must(uuid.NewV7()), TenantID: h.tenant, Role: "admin",
	}))
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func (h *wormHarness) recorded(t *testing.T) time.Time {
	t.Helper()
	var until time.Time
	require.NoError(t, h.pool.QueryRow(h.ctx,
		`SELECT worm_retain_until FROM documents WHERE tenant_id=$1 AND id=$2`, h.tenant, h.doc).Scan(&until))
	return until.UTC()
}

func TestWORMLock_RetainUntilNeverShortens(t *testing.T) {
	h := newWORMHarness(t)
	long := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)
	short := long.Add(-24 * time.Hour)

	rec, _ := h.lock(t, long, "COMPLIANCE")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.True(t, long.Equal(h.recorded(t)))

	rec, out := h.lock(t, short, "COMPLIANCE")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.True(t, long.Equal(h.recorded(t)), "the recorded retention must not move earlier")
	require.Equal(t, long.Format(time.RFC3339), out["worm_retain_until"], "the response reports the effective date")
	require.Equal(t, []string{long.Format(time.RFC3339), long.Format(time.RFC3339)}, h.storage.received,
		"storage is asked for the effective (later) date, never the shorter one")

	later := long.Add(24 * time.Hour)
	rec, _ = h.lock(t, later, "COMPLIANCE")
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, later.Equal(h.recorded(t)), "extending still works")
}

func TestWORMLock_StorageRefusalIs409(t *testing.T) {
	h := newWORMHarness(t)
	until := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)
	rec, _ := h.lock(t, until, "COMPLIANCE")
	require.Equal(t, http.StatusOK, rec.Code)

	h.storage.status = http.StatusConflict // e.g. COMPLIANCE → GOVERNANCE refused
	rec, _ = h.lock(t, until.Add(time.Hour), "GOVERNANCE")
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.True(t, until.Equal(h.recorded(t)), "a refused lock records nothing")
}
