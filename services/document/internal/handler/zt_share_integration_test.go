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
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/aieera/sedoc/pkg/auth"
	pkgcrypto "github.com/aieera/sedoc/pkg/crypto"
	"github.com/aieera/sedoc/pkg/database"
	pkgstorage "github.com/aieera/sedoc/pkg/storage"
	"github.com/aieera/sedoc/pkg/testutil"
	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
	"github.com/aieera/sedoc/services/document/internal/repository"
	"github.com/aieera/sedoc/services/document/internal/service"
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

// newZTTestHandler builds the handler under test on pool, with a document
// service whose policy grants every capability.
func newZTTestHandler(t *testing.T, pool *pgxpool.Pool) *ZTShareHandler {
	t.Helper()
	return newZTHandlerWithPolicy(t, pool, ztSharePolicy{share: true})
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
	svc := service.New(pool, repository.New(pool), ztSharePolicy{share: true}, zerolog.Nop())
	return NewZTShareHandler(pool, svc, s3c, kms, []byte(ztTestPepper))
}

// ztSharePolicy allows every action except "share" when share is false.
type ztSharePolicy struct{ share bool }

func (p ztSharePolicy) allowed(action string) bool { return action != "share" || p.share }

func (p ztSharePolicy) CheckPermission(_ context.Context, in *sedocv1.CheckPermissionRequest, _ ...grpc.CallOption) (*sedocv1.CheckPermissionResponse, error) {
	return &sedocv1.CheckPermissionResponse{Allowed: p.allowed(in.GetAction())}, nil
}
func (p ztSharePolicy) BatchCheckPermission(_ context.Context, in *sedocv1.BatchCheckPermissionRequest, _ ...grpc.CallOption) (*sedocv1.BatchCheckPermissionResponse, error) {
	out := &sedocv1.BatchCheckPermissionResponse{}
	for _, c := range in.GetChecks() {
		out.Results = append(out.Results, &sedocv1.CheckPermissionResponse{Allowed: p.allowed(c.GetAction())})
	}
	return out, nil
}

func ztTokenCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenant uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM zt_share_tokens WHERE tenant_id = $1`, tenant).Scan(&n))
	return n
}

// D-3 — creating a zero-trust share had no permission check (the comment
// promised owner/admin): any member could mint a public link for any
// document id in the tenant, and its manifest leaked the title, MIME and
// sender email. Create now requires "share" on the document, and the
// version must belong to it.
func TestZTCreate_RequiresShare(t *testing.T) {
	ctx, _, pool := newZTDB(t)
	tenant, user, doc, ver := ztSeedTenant(ctx, t, pool)
	_, otherVer := seedDocWithVersion(ctx, t, pool, tenant, user, "Other document")

	viewOnly, _ := ztMuxes(newZTHandlerWithPolicy(t, pool, ztSharePolicy{share: false}))
	rec, _ := ztCreate(t, viewOnly, tenant, user, "member", doc, ver, 0)
	require.Equal(t, http.StatusForbidden, rec.Code, "a caller without share must not mint a zt link: %s", rec.Body.String())
	require.Zero(t, ztTokenCount(t, ctx, pool, tenant))

	sharer, public := ztMuxes(newZTHandlerWithPolicy(t, pool, ztSharePolicy{share: true}))

	// version ∉ document → 404, nothing stored.
	rec, _ = ztCreate(t, sharer, tenant, user, "member", doc, otherVer, 0)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	require.Zero(t, ztTokenCount(t, ctx, pool, tenant))

	// Unknown document → 404.
	rec, _ = ztCreate(t, sharer, tenant, user, "member", uuid.New(), ver, 0)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())

	// Authorized sharer, matching version → created and resolvable.
	rec, created := ztCreate(t, sharer, tenant, user, "member", doc, ver, 0)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, ztTokenCount(t, ctx, pool, tenant))
	require.Equal(t, http.StatusOK, ztGet(public, "/api/v1/zt/"+created.secret(t)+"/manifest").Code)
}

// D-10 — max_views was check-then-increment across two transactions (and
// the increment's error was ignored), so concurrent opens of a
// max_views=1 share all got the bytes. The gate is now one conditional
// UPDATE ... RETURNING.
func TestZTStream_MaxViewsAtomic(t *testing.T) {
	ctx, _, pool := newZTDB(t)
	tenant, user, doc, ver := ztSeedTenant(ctx, t, pool)

	endpoint, access, secretKey, minioClean, err := testutil.NewMinIOContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(minioClean)
	s3c, err := pkgstorage.NewS3Client(endpoint, access, secretKey, false)
	require.NoError(t, err)
	require.NoError(t, s3c.CreateBucket(ctx, ztTestBucket, "us-east-1"))
	ztPutVersionBytes(ctx, t, pool, s3c, nil, tenant, ver, []byte("%PDF-1.7 once"))

	admin, public := ztMuxes(newZTStreamHandler(t, pool, s3c, nil))
	rec, created := ztCreate(t, admin, tenant, user, "member", doc, ver, 1)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	path := "/api/v1/zt/" + created.secret(t) + "/stream"

	const n = 64
	codes := make([]int, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			codes[i] = ztGet(public, path).Code
		}(i)
	}
	close(start)
	wg.Wait()

	served, gone := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			served++
		case http.StatusGone:
			gone++
		default:
			t.Fatalf("unexpected status %d (all: %v)", c, codes)
		}
	}
	require.Equal(t, 1, served, "max_views=1 must serve exactly one concurrent open (codes %v)", codes)
	require.Equal(t, n-1, gone)

	var views int
	require.NoError(t, pool.QueryRow(ctx, `SELECT view_count FROM zt_share_tokens WHERE tenant_id = $1`, tenant).Scan(&views))
	require.Equal(t, 1, views, "view_count must never exceed max_views")
}

// D-10 — the manifest ignored revocation: a revoked share still returned
// 200 with the title, MIME and sender.
func TestZTManifest_Revoked410(t *testing.T) {
	h := newZTHarness(t)
	rec, created := ztCreate(t, h.admin, h.tenant, h.user, "member", h.doc, h.ver, 0)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	manifest := "/api/v1/zt/" + created.secret(t) + "/manifest"
	require.Equal(t, http.StatusOK, ztGet(h.public, manifest).Code)

	revoke := httptest.NewRecorder()
	h.admin.ServeHTTP(revoke, ztAs(httptest.NewRequest(http.MethodPost,
		"/api/v1/admin/share-links/zt/"+created.TokenID+"/revoke", nil), h.tenant, h.user, "member"))
	require.Equal(t, http.StatusNoContent, revoke.Code, revoke.Body.String())

	rec = ztGet(h.public, manifest)
	require.Equal(t, http.StatusGone, rec.Code, "revoked share manifest must be 410")
	require.NotContains(t, rec.Body.String(), "Board minutes", "a revoked manifest must not leak the title")
	require.NotContains(t, rec.Body.String(), "@test.local", "a revoked manifest must not leak the sender")

	// The stream refuses a revoked share at the gate, before storage is
	// touched (a lazy S3 client on an unreachable endpoint proves it).
	lazyS3, err := pkgstorage.NewS3Client("127.0.0.1:1", "x", "y", false)
	require.NoError(t, err)
	_, streamMux := ztMuxes(newZTStreamHandler(t, h.seed, lazyS3, nil))
	rec = ztGet(streamMux, "/api/v1/zt/"+created.secret(t)+"/stream")
	require.Equal(t, http.StatusGone, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "revoked")
}

// newZTHandlerWithPolicy builds the handler with a DocumentService on the
// given policy stub (the create-path authorizer).
func newZTHandlerWithPolicy(t *testing.T, pool *pgxpool.Pool, policy service.PermissionChecker) *ZTShareHandler {
	t.Helper()
	svc := service.New(pool, repository.New(pool), policy, zerolog.Nop())
	return NewZTShareHandler(pool, svc, nil, nil, []byte(ztTestPepper))
}

// D-3/D-4 follow-up — a trashed (or disposal-pending) document stayed
// readable through an outstanding zt link until it expired; the share-link
// download path already refuses that. Manifest and stream now answer 410
// "deleted", and no view is consumed.
func TestZTStream_DeletedDocument410(t *testing.T) {
	h := newZTHarness(t)
	rec, created := ztCreate(t, h.admin, h.tenant, h.user, "member", h.doc, h.ver, 0)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	secret := created.secret(t)

	_, err := h.seed.Exec(h.ctx, `UPDATE documents SET deleted_at = now() WHERE tenant_id = $1 AND id = $2`, h.tenant, h.doc)
	require.NoError(t, err)

	rec = ztGet(h.public, "/api/v1/zt/"+secret+"/manifest")
	require.Equal(t, http.StatusGone, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "deleted")
	require.NotContains(t, rec.Body.String(), "Board minutes")

	lazyS3, err := pkgstorage.NewS3Client("127.0.0.1:1", "x", "y", false)
	require.NoError(t, err)
	_, streamMux := ztMuxes(newZTStreamHandler(t, h.seed, lazyS3, nil))
	rec = ztGet(streamMux, "/api/v1/zt/"+secret+"/stream")
	require.Equal(t, http.StatusGone, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "deleted")

	var views int
	require.NoError(t, h.seed.QueryRow(h.ctx, `SELECT view_count FROM zt_share_tokens WHERE tenant_id = $1`, h.tenant).Scan(&views))
	require.Zero(t, views, "a deleted document must not consume a view")
}

// The recipient telemetry endpoint accepted writes for revoked and expired
// links (unauthenticated writes into the sender's activity timeline after
// the share was killed). Same gate as manifest/stream: 410, nothing stored.
func TestZTTelemetry_RevokedRejected(t *testing.T) {
	h := newZTHarness(t)
	post := func(secret string) int {
		rec := httptest.NewRecorder()
		h.public.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/zt/"+secret+"/telemetry",
			bytes.NewReader([]byte(`{"event_type":"page_view","page_number":1}`))))
		return rec.Code
	}
	count := func() int {
		var n int
		require.NoError(t, h.seed.QueryRow(h.ctx, `SELECT count(*) FROM zt_share_telemetry WHERE tenant_id = $1`, h.tenant).Scan(&n))
		return n
	}

	rec, revoked := ztCreate(t, h.admin, h.tenant, h.user, "member", h.doc, h.ver, 0)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, http.StatusNoContent, post(revoked.secret(t)))
	require.Equal(t, 1, count())

	rv := httptest.NewRecorder()
	h.admin.ServeHTTP(rv, ztAs(httptest.NewRequest(http.MethodPost,
		"/api/v1/admin/share-links/zt/"+revoked.TokenID+"/revoke", nil), h.tenant, h.user, "member"))
	require.Equal(t, http.StatusNoContent, rv.Code)
	require.Equal(t, http.StatusGone, post(revoked.secret(t)), "revoked link must not accept telemetry")

	rec, expired := ztCreate(t, h.admin, h.tenant, h.user, "member", h.doc, h.ver, 0)
	require.Equal(t, http.StatusOK, rec.Code)
	_, err := h.seed.Exec(h.ctx, `UPDATE zt_share_tokens SET expires_at = now() - interval '1 minute'
		WHERE tenant_id = $1 AND token_id = $2`, h.tenant, uuid.MustParse(expired.TokenID))
	require.NoError(t, err)
	require.Equal(t, http.StatusGone, post(expired.secret(t)), "expired link must not accept telemetry")

	require.Equal(t, 1, count(), "no telemetry stored after revoke/expiry")
}
