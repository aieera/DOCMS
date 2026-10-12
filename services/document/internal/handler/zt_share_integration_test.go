//go:build integration
// +build integration

// Zero-trust view-only share (ADR 0098) — public token surfaces.
//
// Shared harness + the D-series regression tests for zt_share_handler.go.
// The superuser pool is used here (behaviour tests); the production-role
// variant lives in zt_share_prodposture_test.go.
package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/aieera/sedoc/pkg/auth"
	pkgcrypto "github.com/aieera/sedoc/pkg/crypto"
	"github.com/aieera/sedoc/pkg/database"
	pkgstorage "github.com/aieera/sedoc/pkg/storage"
	"github.com/aieera/sedoc/pkg/testutil"
	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
)

const ztTestPepper = "zt-test-pepper"

type ztHarness struct {
	ctx    context.Context
	dsn    string
	seed   *pgxpool.Pool // seeding / assertions (superuser)
	tenant uuid.UUID
	user   uuid.UUID
	doc    uuid.UUID
	ver    uuid.UUID
	admin  *http.ServeMux
	public *http.ServeMux
}

// ztSeedTenant seeds an org + a member user + one document with a head
// version, returning (tenant, user, doc, version).
func ztSeedTenant(ctx context.Context, t *testing.T, super *pgxpool.Pool) (uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	tenant, user := uuid.New(), uuid.Must(uuid.NewV7())
	_, err := super.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ($1, 'zt', $2)`,
		tenant, "zt-"+tenant.String()[:8])
	require.NoError(t, err)
	_, err = super.Exec(ctx, `INSERT INTO users (tenant_id, id, email, display_name, role, status)
		VALUES ($1, $2, $3, 'Sender', 'member', 'active')`, tenant, user, "sender-"+user.String()[:8]+"@test.local")
	require.NoError(t, err)
	doc, ver := seedDocWithVersion(ctx, t, super, tenant, user, "Board minutes")
	return tenant, user, doc, ver
}

// newZTDB starts Postgres with the full document migration chain and
// returns the DSN plus a superuser pool.
func newZTDB(t *testing.T) (context.Context, string, *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	t.Cleanup(cancel)
	dsn, clean, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(clean)
	require.NoError(t, database.RunMigrations(dsn, "../../migrations"))
	cfg := database.DefaultPoolConfig()
	cfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return ctx, dsn, pool
}

func newZTHarness(t *testing.T) *ztHarness {
	t.Helper()
	ctx, dsn, pool := newZTDB(t)
	tenant, user, doc, ver := ztSeedTenant(ctx, t, pool)
	h := &ztHarness{ctx: ctx, dsn: dsn, seed: pool, tenant: tenant, user: user, doc: doc, ver: ver}
	h.admin, h.public = ztMuxes(newZTTestHandler(t, pool))
	return h
}

func ztMuxes(zh *ZTShareHandler) (*http.ServeMux, *http.ServeMux) {
	admin, public := http.NewServeMux(), http.NewServeMux()
	zh.RegisterAdmin(admin)
	zh.RegisterPublic(public)
	return admin, public
}

// ztAs returns a request carrying a session identity (what SessionAuth
// stamps on ctx).
func ztAs(req *http.Request, tenant, user uuid.UUID, role string) *http.Request {
	return req.WithContext(auth.WithUser(auth.SetTenantID(req.Context(), tenant),
		auth.UserInfo{ID: user, TenantID: tenant, Role: role, Email: "sender@test.local"}))
}

type ztCreated struct {
	TokenID  string `json:"token_id"`
	ShareURL string `json:"share_url"`
}

// secret returns the URL pre-image (the last path segment of share_url).
func (c ztCreated) secret(t *testing.T) string {
	t.Helper()
	u, err := url.Parse(c.ShareURL)
	require.NoError(t, err)
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	require.Len(t, parts, 2, "share_url path must be /zt/{token}")
	require.Equal(t, "zt", parts[0])
	return parts[1]
}

func ztCreate(t *testing.T, mux *http.ServeMux, tenant, user uuid.UUID, role string, doc, ver uuid.UUID, maxViews int) (*httptest.ResponseRecorder, ztCreated) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"document_id": doc.String(), "version_id": ver.String(),
		"recipient_email": "outside@example.com", "expires_in_hours": 24, "max_views": maxViews,
	})
	req := ztAs(httptest.NewRequest(http.MethodPost, "/api/v1/admin/share-links/zt", bytes.NewReader(body)), tenant, user, role)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var out ztCreated
	if rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	}
	return rec, out
}

func ztGet(mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func ztPepperHash(id uuid.UUID) []byte {
	hh := sha256.New()
	hh.Write(id[:])
	hh.Write([]byte(ztTestPepper))
	return hh.Sum(nil)
}

// ztRollbackBelow rolls the migration chain back until the applied version
// is below v (so a test can seed rows in the pre-v shape and re-apply).
func ztRollbackBelow(t *testing.T, ctx context.Context, dsn string, pool *pgxpool.Pool, v int) {
	t.Helper()
	for i := 0; i < 50; i++ {
		var cur int
		require.NoError(t, pool.QueryRow(ctx, `SELECT version FROM schema_migrations`).Scan(&cur))
		if cur < v {
			return
		}
		require.NoError(t, database.RollbackMigration(dsn, "../../migrations"))
	}
	t.Fatalf("could not roll back below migration %d", v)
}

// D-9 — the zt token id was stored in cleartext beside its hash: anyone with
// read access to zt_share_tokens (DB reader, backup, support tooling) could
// rebuild every live share URL. Only the hash may be stored; the URL carries
// the pre-image. Links minted before migration 000113 must keep working.
func TestZTToken_OnlyHashStored(t *testing.T) {
	ctx, dsn, pool := newZTDB(t)
	tenant, user, doc, ver := ztSeedTenant(ctx, t, pool)

	// A link minted the pre-000113 way: token_id IS the URL pre-image and
	// token_hash = sha256(token_id || pepper); one telemetry row on it.
	ztRollbackBelow(t, ctx, dsn, pool, 113)
	legacy := uuid.New()
	_, err := pool.Exec(ctx, `
		INSERT INTO zt_share_tokens
			(tenant_id, token_id, token_hash, document_id, version_id, recipient_email,
			 watermark_text, created_by, expires_at, max_views, session_key_seed)
		VALUES ($1, $2, $3, $4, $5, 'legacy@example.com', 'legacy', $6, now() + interval '1 day', 0, '\x00'::bytea)`,
		tenant, legacy, ztPepperHash(legacy), doc, ver, user)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO zt_share_telemetry (tenant_id, token_id, event_type) VALUES ($1, $2, 'page_view')`,
		tenant, legacy)
	require.NoError(t, err)
	require.NoError(t, database.RunMigrations(dsn, "../../migrations"))

	admin, public := ztMuxes(newZTTestHandler(t, pool))

	// The legacy row no longer carries its pre-image, its telemetry moved
	// with it, and the URL the recipient already holds still resolves.
	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM zt_share_tokens WHERE token_id = $1`, legacy).Scan(&n))
	require.Zero(t, n, "migration 000113 must rotate the stored token_id off the URL pre-image")
	var rowID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `SELECT token_id FROM zt_share_tokens WHERE token_hash = $1`, ztPepperHash(legacy)).Scan(&rowID))
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM zt_share_telemetry WHERE tenant_id = $1 AND token_id = $2`, tenant, rowID).Scan(&n))
	require.Equal(t, 1, n, "telemetry must follow the rotated row id")
	rec := ztGet(public, "/api/v1/zt/"+legacy.String()+"/manifest")
	require.Equal(t, http.StatusOK, rec.Code, "pre-000113 links must keep working: %s", rec.Body.String())

	// A new share: the URL pre-image appears nowhere in the row.
	rec, created := ztCreate(t, admin, tenant, user, "member", doc, ver, 0)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	secret := created.secret(t)
	secretID, err := uuid.Parse(secret)
	require.NoError(t, err)
	require.NotEqual(t, secret, created.TokenID, "the admin handle must not be the URL credential")
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*) FROM zt_share_tokens
		 WHERE token_id::text = $1 OR position($1 in watermark_text) > 0
		    OR position(left($1, 8) in watermark_text) > 0`, secret).Scan(&n))
	require.Zero(t, n, "the URL pre-image (or a prefix of it) must not be stored")
	var storedHash []byte
	require.NoError(t, pool.QueryRow(ctx, `SELECT token_hash FROM zt_share_tokens WHERE token_id = $1`,
		uuid.MustParse(created.TokenID)).Scan(&storedHash))
	require.Equal(t, ztPepperHash(secretID), storedHash)

	require.Equal(t, http.StatusOK, ztGet(public, "/api/v1/zt/"+secret+"/manifest").Code)
	require.Equal(t, http.StatusNotFound, ztGet(public, "/api/v1/zt/"+created.TokenID+"/manifest").Code,
		"the stored row id must not work as a credential")
}

// newZTTestHandler builds the handler under test on pool.
func newZTTestHandler(t *testing.T, pool *pgxpool.Pool) *ZTShareHandler {
	t.Helper()
	return NewZTShareHandler(pool, nil, nil, []byte(ztTestPepper))
}

// ztPolicy mirrors the real policy service's stance on a sessionless
// caller: a check whose subject is the nil user is denied; everything else
// is allowed.
type ztPolicy struct{}

func (ztPolicy) CheckPermission(_ context.Context, in *sedocv1.CheckPermissionRequest, _ ...grpc.CallOption) (*sedocv1.CheckPermissionResponse, error) {
	return &sedocv1.CheckPermissionResponse{Allowed: in.GetSubjectId() != uuid.Nil.String()}, nil
}
func (ztPolicy) BatchCheckPermission(_ context.Context, in *sedocv1.BatchCheckPermissionRequest, _ ...grpc.CallOption) (*sedocv1.BatchCheckPermissionResponse, error) {
	out := &sedocv1.BatchCheckPermissionResponse{}
	for _, c := range in.GetChecks() {
		out.Results = append(out.Results, &sedocv1.CheckPermissionResponse{Allowed: c.GetSubjectId() != uuid.Nil.String()})
	}
	return out, nil
}

const ztTestBucket = "zt-stream-test"

// ztPutVersionBytes stores plaintext as the version's blob — envelope
// encrypted under kms when kms is non-nil — and points content_blobs at it.
func ztPutVersionBytes(ctx context.Context, t *testing.T, pool *pgxpool.Pool, s3c *pkgstorage.S3Client, kms pkgcrypto.KeyManager, tenant, ver uuid.UUID, plaintext []byte) {
	t.Helper()
	key := tenant.String() + "/zt/" + ver.String()
	body, kekID := plaintext, ""
	var wrapped, nonce []byte
	if kms != nil {
		dek, err := pkgcrypto.GenerateDEK()
		require.NoError(t, err)
		kekID = "tenant/" + tenant.String()
		var ct []byte
		ct, nonce, err = pkgcrypto.EncryptData(plaintext, dek)
		require.NoError(t, err)
		wrapped, err = kms.EncryptDataKey(ctx, kekID, dek)
		require.NoError(t, err)
		body = ct
	}
	require.NoError(t, s3c.PutObject(ctx, ztTestBucket, key, bytes.NewReader(body), int64(len(body)), "application/pdf"))
	_, err := pool.Exec(ctx, `
		UPDATE content_blobs b
		   SET storage_bucket = $3, storage_key = $4, kek_id = NULLIF($5, ''),
		       encrypted_dek = $6, dek_nonce = $7, size_bytes = $8
		  FROM document_versions v
		 WHERE v.tenant_id = $1 AND v.id = $2
		   AND b.tenant_id = v.tenant_id AND b.id = v.content_blob_id`,
		tenant, ver, ztTestBucket, key, kekID, wrapped, nonce, len(body))
	require.NoError(t, err)
}

// D-4 — the recipient viewer has no session. The stream route handed the
// request to the session-oriented storage proxy: the per-document policy
// check runs with a nil user (denied by the real policy), and an encrypted
// blob is answered with a redirect to the session-only decrypt-stream
// route. The token is the authorization: bytes must be served (decrypted
// under the tenant's KEK) with no session at all.
func TestZTStream_NoSessionServesBytes(t *testing.T) {
	ctx, _, pool := newZTDB(t)
	tenant, user, doc, ver := ztSeedTenant(ctx, t, pool)
	plainDoc, plainVer := seedDocWithVersion(ctx, t, pool, tenant, user, "Plain")

	endpoint, access, secretKey, minioClean, err := testutil.NewMinIOContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(minioClean)
	s3c, err := pkgstorage.NewS3Client(endpoint, access, secretKey, false)
	require.NoError(t, err)
	require.NoError(t, s3c.CreateBucket(ctx, ztTestBucket, "us-east-1"))
	kms, err := pkgcrypto.NewLocalKeyManager(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)), nil)
	require.NoError(t, err)

	encrypted := []byte("%PDF-1.7 encrypted-at-rest board minutes")
	plain := []byte("%PDF-1.7 unencrypted appendix")
	ztPutVersionBytes(ctx, t, pool, s3c, kms, tenant, ver, encrypted)
	ztPutVersionBytes(ctx, t, pool, s3c, nil, tenant, plainVer, plain)

	admin, public := ztMuxes(newZTStreamHandler(t, pool, s3c, kms))

	for _, tc := range []struct {
		name      string
		doc, ver  uuid.UUID
		wantBytes []byte
	}{
		{"envelope-encrypted blob", doc, ver, encrypted},
		{"unencrypted blob", plainDoc, plainVer, plain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, created := ztCreate(t, admin, tenant, user, "member", tc.doc, tc.ver, 0)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			// No session, no tenant header: just the token.
			rec = ztGet(public, "/api/v1/zt/"+created.secret(t)+"/stream")
			require.Equal(t, http.StatusOK, rec.Code, "sessionless recipient must get bytes, got %d %q", rec.Code, rec.Body.String())
			require.Equal(t, tc.wantBytes, rec.Body.Bytes())
			require.Equal(t, "application/pdf", rec.Header().Get("Content-Type"))
			require.Contains(t, rec.Header().Get("Cache-Control"), "no-store")
			require.Empty(t, rec.Header().Get("Location"), "must not redirect to a session-only route")
		})
	}
}

// newZTStreamHandler builds the handler with the byte-serving dependencies.
func newZTStreamHandler(t *testing.T, pool *pgxpool.Pool, s3c *pkgstorage.S3Client, kms pkgcrypto.KeyManager) *ZTShareHandler {
	t.Helper()
	return NewZTShareHandler(pool, s3c, kms, []byte(ztTestPepper))
}
