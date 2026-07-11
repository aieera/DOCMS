//go:build integration
// +build integration

// Both Office-editing save paths, end to end against real Postgres
// (full document migrations) + real MinIO + the real DocumentService:
//
//   WOPI (Collabora):   CheckFileInfo → GetFile → Lock → PutFile
//                       → NEW immutable version + dms.version.uploaded.v1
//                       outbox event (the search-indexing trigger);
//                       non-holder PutFile 409s; legal-hold blocks save.
//
//   OnlyOffice:         callback status 2 (ready-to-save) and 6 (force
//                       save) download the edited bytes from the DS URL
//                       and commit through the SAME write path; status 4
//                       writes nothing; a bad callback JWT is 403; a
//                       missing/forged access_token is rejected; a WOPI
//                       lock held by another session rejects save-back.
//
// The storage gRPC surface is faked at the proto client interface (the
// scan/encrypt pipeline behind it belongs to services/storage's own
// suite — internal packages can't cross service boundaries); the fake
// preserves the contract: initiate hands out real MinIO coordinates,
// complete verifies the object landed and creates the content_blobs row.
package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
	pkgstorage "github.com/aieera/sedoc/pkg/storage"
	"github.com/aieera/sedoc/pkg/testutil"
	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
	"github.com/aieera/sedoc/services/document/internal/repository"
	"github.com/aieera/sedoc/services/document/internal/service"
)

const officeTestBucket = "sedoc-office-test"

// ---- policy stub (same shape as the service-suite harness) ---------------

type allowPolicy struct{}

func (allowPolicy) CheckPermission(_ context.Context, _ *sedocv1.CheckPermissionRequest, _ ...grpc.CallOption) (*sedocv1.CheckPermissionResponse, error) {
	return &sedocv1.CheckPermissionResponse{Allowed: true}, nil
}
func (allowPolicy) BatchCheckPermission(_ context.Context, in *sedocv1.BatchCheckPermissionRequest, _ ...grpc.CallOption) (*sedocv1.BatchCheckPermissionResponse, error) {
	out := &sedocv1.BatchCheckPermissionResponse{}
	for range in.GetChecks() {
		out.Results = append(out.Results, &sedocv1.CheckPermissionResponse{Allowed: true})
	}
	return out, nil
}

// ---- fake storage gRPC client ---------------------------------------------
//
// Mirrors the real service's contract at the client interface: initiate
// returns MinIO coordinates keyed like `{tenant}/{Y}/{M}/{upload}/{name}`,
// complete verifies the object exists at them and inserts the
// content_blobs row (the piece CreateVersion needs). Scan/encrypt/dedup
// live in services/storage's own tests.
type fakeStorageClient struct {
	pool *pgxpool.Pool
	s3   *pkgstorage.S3Client

	mu      sync.Mutex
	pending map[string]pendingUpload
}

type pendingUpload struct {
	tenant   uuid.UUID
	bucket   string
	key      string
	mime     string
	sha      string
	declared int64
}

func tenantFromMD(ctx context.Context) uuid.UUID {
	md, _ := metadata.FromOutgoingContext(ctx)
	if vs := md.Get("x-tenant-id"); len(vs) == 1 {
		if id, err := uuid.Parse(vs[0]); err == nil {
			return id
		}
	}
	return uuid.Nil
}

func (f *fakeStorageClient) InitiateUpload(ctx context.Context, in *sedocv1.InitiateUploadRequest, _ ...grpc.CallOption) (*sedocv1.InitiateUploadResponse, error) {
	tenant := tenantFromMD(ctx)
	if tenant == uuid.Nil {
		return nil, fmt.Errorf("no x-tenant-id metadata")
	}
	uploadID := uuid.New()
	key := fmt.Sprintf("%s/2026/07/%s/%s", tenant, uploadID, in.GetFilename())
	f.mu.Lock()
	f.pending[uploadID.String()] = pendingUpload{
		tenant: tenant, bucket: officeTestBucket, key: key,
		mime: in.GetMimeType(), sha: in.GetChecksumSha256(), declared: in.GetSizeBytes(),
	}
	f.mu.Unlock()
	return &sedocv1.InitiateUploadResponse{
		UploadId:      uploadID.String(),
		StorageBucket: officeTestBucket,
		StorageKey:    key,
	}, nil
}

func (f *fakeStorageClient) CompleteUpload(ctx context.Context, in *sedocv1.CompleteUploadRequest, _ ...grpc.CallOption) (*sedocv1.CompleteUploadResponse, error) {
	f.mu.Lock()
	p, ok := f.pending[in.GetUploadId()]
	delete(f.pending, in.GetUploadId())
	f.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("unknown upload id %s", in.GetUploadId())
	}
	// The object must actually be at the coordinates initiate handed out.
	info, err := f.s3.GetObjectInfo(ctx, p.bucket, p.key)
	if err != nil {
		return nil, fmt.Errorf("object not found at %s/%s: %w", p.bucket, p.key, err)
	}
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO content_blobs (id, tenant_id, sha256_hash, storage_region, storage_bucket, storage_key, size_bytes, mime_type)
		VALUES ($1, $2, $3, 'us-east-1', $4, $5, $6, $7)
		ON CONFLICT (tenant_id, sha256_hash) DO NOTHING`,
		uuid.New(), p.tenant, in.GetChecksumSha256(), p.bucket, p.key, info.Size, p.mime); err != nil {
		return nil, fmt.Errorf("insert content_blob: %w", err)
	}
	return &sedocv1.CompleteUploadResponse{
		StorageBucket:  p.bucket,
		StorageKey:     p.key,
		SizeBytes:      info.Size,
		ChecksumSha256: in.GetChecksumSha256(),
	}, nil
}

func (f *fakeStorageClient) AbortUpload(context.Context, *sedocv1.AbortUploadRequest, ...grpc.CallOption) (*sedocv1.AbortUploadResponse, error) {
	return nil, fmt.Errorf("unimplemented")
}
func (f *fakeStorageClient) GetDownloadURL(context.Context, *sedocv1.GetDownloadURLRequest, ...grpc.CallOption) (*sedocv1.GetDownloadURLResponse, error) {
	return nil, fmt.Errorf("unimplemented")
}
func (f *fakeStorageClient) GetPreviewURL(context.Context, *sedocv1.GetPreviewURLRequest, ...grpc.CallOption) (*sedocv1.GetPreviewURLResponse, error) {
	return nil, fmt.Errorf("unimplemented")
}
func (f *fakeStorageClient) GetScanStatus(context.Context, *sedocv1.GetScanStatusRequest, ...grpc.CallOption) (*sedocv1.ScanStatus, error) {
	return nil, fmt.Errorf("unimplemented")
}
func (f *fakeStorageClient) RequestLifecycle(context.Context, *sedocv1.RequestLifecycleRequest, ...grpc.CallOption) (*sedocv1.LifecycleJob, error) {
	return nil, fmt.Errorf("unimplemented")
}

// ---- harness ---------------------------------------------------------------

type officeHarness struct {
	ctx      context.Context
	pool     *pgxpool.Pool
	s3       *pkgstorage.S3Client
	rdb      *redis.Client
	mr       *miniredis.Miniredis
	svc      *service.DocumentService
	resolver *DBWOPIResolver
	tenant   uuid.UUID
	user     uuid.UUID
	docID    uuid.UUID
	v1       uuid.UUID // seeded first version id (the WOPI file_id)
	v1Bytes  []byte
}

func newOfficeHarness(t *testing.T) *officeHarness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	t.Cleanup(cancel)

	dsn, pgClean, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(pgClean)
	require.NoError(t, database.RunMigrations(dsn, "../../migrations"))
	poolCfg := database.DefaultPoolConfig()
	poolCfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, poolCfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	endpoint, access, secret, minioClean, err := testutil.NewMinIOContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(minioClean)
	s3c, err := pkgstorage.NewS3Client(endpoint, access, secret, false)
	require.NoError(t, err)
	require.NoError(t, s3c.CreateBucket(ctx, officeTestBucket, "us-east-1"))

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	repos := repository.New(pool)
	svc := service.New(pool, repos, allowPolicy{}, zerolog.Nop())

	tenant := uuid.Must(uuid.NewV7())
	user := uuid.Must(uuid.NewV7())
	_, err = pool.Exec(ctx, `
		INSERT INTO organizations (id, name, slug, plan, primary_region)
		VALUES ($1, 'Office Test Org', $2, 'standard', 'us-east-1')`,
		tenant, "t-"+tenant.String()[:8])
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO users (tenant_id, id, email, display_name, role, status)
		VALUES ($1, $2, $3, 'Office Editor', 'admin', 'active')`,
		tenant, user, "u-"+user.String()[:8]+"@test.local")
	require.NoError(t, err)

	ictx := auth.WithUser(auth.SetTenantID(ctx, tenant), auth.UserInfo{ID: user, TenantID: tenant, Role: "admin"})

	// Workspace + folder + document.
	wsID := uuid.Must(uuid.NewV7())
	_, err = pool.Exec(ctx, `
		INSERT INTO workspaces (tenant_id, id, name, region_pin, created_by)
		VALUES ($1, $2, 'ws', 'us-east-1', $3)`, tenant, wsID, user)
	require.NoError(t, err)
	folder, err := svc.CreateFolder(ictx, &service.CreateFolderInput{WorkspaceID: wsID, Name: "Root"})
	require.NoError(t, err)
	doc, err := svc.CreateDocument(ictx, &service.CreateDocumentInput{
		UpdatedBy:   user,
		WorkspaceID: wsID,
		FolderID:    folder.ID,
		Title:       "Quarterly report", // no extension — resolver must add .docx
		RegionPin:   "us-east-1",
	})
	require.NoError(t, err)

	// Seed v1: object in MinIO + blob row + real CreateVersion.
	v1Bytes := []byte("original office document bytes v1")
	sum := sha256.Sum256(v1Bytes)
	v1SHA := hex.EncodeToString(sum[:])
	v1Key := fmt.Sprintf("%s/2026/07/%s/quarterly.docx", tenant, uuid.New())
	require.NoError(t, s3c.PutObject(ctx, officeTestBucket, v1Key,
		bytes.NewReader(v1Bytes), int64(len(v1Bytes)), officeDocxMime))
	blobID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO content_blobs (id, tenant_id, sha256_hash, storage_region, storage_bucket, storage_key, size_bytes, mime_type)
		VALUES ($1, $2, $3, 'us-east-1', $4, $5, $6, $7)`,
		blobID, tenant, v1SHA, officeTestBucket, v1Key, len(v1Bytes), officeDocxMime)
	require.NoError(t, err)
	v1, err := svc.CreateVersion(ictx, &service.CreateVersionInput{
		DocumentID:    doc.ID,
		ContentBlobID: blobID,
		SizeBytes:     int64(len(v1Bytes)),
		MimeType:      officeDocxMime,
		SHA256Hash:    v1SHA,
		ChangeSummary: "initial upload",
	})
	require.NoError(t, err)

	fake := &fakeStorageClient{pool: pool, s3: s3c, pending: map[string]pendingUpload{}}
	resolver := NewDBWOPIResolver(pool, s3c, nil, fake, svc, zerolog.Nop())

	return &officeHarness{
		ctx: ctx, pool: pool, s3: s3c, rdb: rdb, mr: mr, svc: svc,
		resolver: resolver, tenant: tenant, user: user, docID: doc.ID,
		v1: v1.ID, v1Bytes: v1Bytes,
	}
}

const officeDocxMime = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

func (h *officeHarness) versionCount(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, h.pool.QueryRow(h.ctx,
		`SELECT count(*) FROM document_versions WHERE tenant_id=$1 AND document_id=$2`,
		h.tenant, h.docID).Scan(&n))
	return n
}

func (h *officeHarness) headVersion(t *testing.T) (versionID uuid.UUID, versionNumber int, sha string) {
	t.Helper()
	require.NoError(t, h.pool.QueryRow(h.ctx, `
		SELECT v.id, v.version_number, v.sha256_hash
		FROM documents d JOIN document_versions v
		  ON v.tenant_id = d.tenant_id AND v.id = d.current_version_id
		WHERE d.tenant_id=$1 AND d.id=$2`,
		h.tenant, h.docID).Scan(&versionID, &versionNumber, &sha))
	return
}

func (h *officeHarness) uploadedEventCount(t *testing.T, aggregateID uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, h.pool.QueryRow(h.ctx, `
		SELECT count(*) FROM outbox
		WHERE tenant_id=$1 AND event_type='dms.version.uploaded.v1' AND aggregate_id=$2`,
		h.tenant, aggregateID).Scan(&n))
	return n
}

// ---- WOPI end-to-end --------------------------------------------------------

func TestWOPI_EditRoundTrip_CreatesNewVersion(t *testing.T) {
	h := newOfficeHarness(t)
	const secret = "wopi-integration-secret"
	t.Setenv("SEDOC_WOPI_SECRET", secret)

	wopiH := NewWOPIHandler(h.rdb, zerolog.Nop())
	wopiH.FileResolver = h.resolver
	mux := http.NewServeMux()
	wopiH.Register(mux)

	token := IssueWOPIToken(secret, WOPIClaims{
		TenantID: h.tenant, UserID: h.user, FileID: h.v1,
		ExpiresAt: time.Now().Add(time.Hour), CanWrite: true,
	})
	base := "/wopi/files/" + h.v1.String()

	// 1. CheckFileInfo — resolver is wired, metadata is real.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, base+"?access_token="+token, nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var info WOPIFileInfo
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &info))
	require.Equal(t, "Quarterly report.docx", info.BaseFileName, "extension derived from mime")
	require.Equal(t, int64(len(h.v1Bytes)), info.Size)
	require.True(t, info.UserCanWrite)
	require.False(t, info.ReadOnly)

	// 2. GetFile — the seeded bytes come back.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, base+"/contents?access_token="+token, nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, h.v1Bytes, rec.Body.Bytes())

	// 3. Lock (as Collabora does before writing).
	lockReq := httptest.NewRequest(http.MethodPost, base+"?access_token="+token, nil)
	lockReq.Header.Set("X-WOPI-Override", "LOCK")
	lockReq.Header.Set("X-WOPI-Lock", "collabora-session-1")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, lockReq)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// 4. A NON-HOLDER PutFile (wrong lock) must 409 and write nothing.
	edited := []byte("EDITED in collabora — new content v2")
	badPut := httptest.NewRequest(http.MethodPost, base+"/contents?access_token="+token, bytes.NewReader(edited))
	badPut.Header.Set("X-WOPI-Lock", "some-other-session")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, badPut)
	require.Equal(t, http.StatusConflict, rec.Code, "non-holder save-back must be rejected")
	require.Equal(t, "collabora-session-1", rec.Header().Get("X-WOPI-Lock"))
	require.Equal(t, 1, h.versionCount(t), "rejected PutFile must not create a version")

	// 5. Holder PutFile → 200 → NEW version through the normal write path.
	putReq := httptest.NewRequest(http.MethodPost, base+"/contents?access_token="+token, bytes.NewReader(edited))
	putReq.Header.Set("X-WOPI-Lock", "collabora-session-1")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, putReq)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	require.Equal(t, 2, h.versionCount(t), "PutFile must append a new version, never overwrite")
	headID, headNum, headSHA := h.headVersion(t)
	require.Equal(t, 2, headNum, "document head must repoint to v2")
	sum := sha256.Sum256(edited)
	require.Equal(t, hex.EncodeToString(sum[:]), headSHA, "v2 carries the edited bytes' hash")
	require.NotEqual(t, h.v1, headID)

	// The canonical event that drives search indexing / OCR / preview.
	require.Equal(t, 1, h.uploadedEventCount(t, headID),
		"save-back must emit dms.version.uploaded.v1 for the new version")

	// And the edited bytes are really in object storage at the new blob.
	var bucket, key string
	require.NoError(t, h.pool.QueryRow(h.ctx, `
		SELECT b.storage_bucket, b.storage_key
		FROM document_versions v JOIN content_blobs b
		  ON b.tenant_id=v.tenant_id AND b.id=v.content_blob_id
		WHERE v.tenant_id=$1 AND v.id=$2`, h.tenant, headID).Scan(&bucket, &key))
	obj, err := h.s3.GetObject(h.ctx, bucket, key)
	require.NoError(t, err)
	got, err := io.ReadAll(obj)
	_ = obj.Close()
	require.NoError(t, err)
	require.Equal(t, edited, got)
}

func TestWOPI_LegalHold_BlocksSaveAndMarksReadOnly(t *testing.T) {
	h := newOfficeHarness(t)
	const secret = "wopi-integration-secret"
	t.Setenv("SEDOC_WOPI_SECRET", secret)

	_, err := h.pool.Exec(h.ctx,
		`UPDATE documents SET lifecycle_state='legal_hold' WHERE tenant_id=$1 AND id=$2`,
		h.tenant, h.docID)
	require.NoError(t, err)

	wopiH := NewWOPIHandler(h.rdb, zerolog.Nop())
	wopiH.FileResolver = h.resolver
	mux := http.NewServeMux()
	wopiH.Register(mux)
	token := IssueWOPIToken(secret, WOPIClaims{
		TenantID: h.tenant, UserID: h.user, FileID: h.v1,
		ExpiresAt: time.Now().Add(time.Hour), CanWrite: true,
	})
	base := "/wopi/files/" + h.v1.String()

	// CheckFileInfo surfaces the freeze so the editor opens read-only.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, base+"?access_token="+token, nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var info WOPIFileInfo
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &info))
	require.False(t, info.UserCanWrite, "legal hold must surface as read-only")

	// And a direct PutFile is refused by the lifecycle gate.
	putReq := httptest.NewRequest(http.MethodPost, base+"/contents?access_token="+token,
		strings.NewReader("attempt to change held content"))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, putReq)
	require.Equal(t, http.StatusInternalServerError, rec.Code,
		"save on a legal-hold document must fail")
	require.Equal(t, 1, h.versionCount(t), "no version may be created on a held document")
}

// ---- OnlyOffice end-to-end --------------------------------------------------

// onlyOfficeCallbackHarness bundles the pieces the callback tests share.
func onlyOfficeSetup(t *testing.T, h *officeHarness, editedBytes []byte) (mux *http.ServeMux, callbackPath string, jwtSecret, dlURL string) {
	t.Helper()
	jwtSecret = "onlyoffice-integration-secret"
	t.Setenv("SEDOC_ONLYOFFICE_JWT", jwtSecret)

	// Fake Document Server: serves the edited file at /cache/edited.docx.
	ds := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cache/edited.docx" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(editedBytes)
	}))
	t.Cleanup(ds.Close)
	t.Setenv("SEDOC_ONLYOFFICE_URL", ds.URL) // pins the SSRF allow-list to this host

	ooh := NewOnlyOfficeHandler(zerolog.Nop())
	ooh.Saver = h.resolver
	ooh.Redis = h.rdb
	mux = http.NewServeMux()
	ooh.Register(mux)

	accessToken := IssueWOPIToken(jwtSecret, WOPIClaims{
		TenantID: h.tenant, UserID: h.user, FileID: h.v1,
		ExpiresAt: time.Now().Add(time.Hour), CanWrite: true,
	})
	callbackPath = "/api/v1/documents/" + h.docID.String() + "/versions/" + h.v1.String() +
		"/onlyoffice/callback?access_token=" + accessToken
	return mux, callbackPath, jwtSecret, ds.URL + "/cache/edited.docx"
}

// postCallback signs the event body with the DS JWT (body-form token) and
// posts it, returning the recorder.
func postCallback(t *testing.T, mux *http.ServeMux, path, jwtSecret string, status int, url string) *httptest.ResponseRecorder {
	t.Helper()
	event := map[string]any{"status": status, "url": url, "key": "k"}
	tok, err := hs256(event, jwtSecret)
	require.NoError(t, err)
	event["token"] = tok
	body, _ := json.Marshal(event)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func callbackError(t *testing.T, rec *httptest.ResponseRecorder) int {
	t.Helper()
	var out struct {
		Error int `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out.Error
}

func TestOnlyOffice_Status2_SavesNewVersion(t *testing.T) {
	h := newOfficeHarness(t)
	edited := []byte("EDITED in onlyoffice — status 2 content")
	mux, path, jwtSecret, dlURL := onlyOfficeSetup(t, h, edited)

	rec := postCallback(t, mux, path, jwtSecret, 2, dlURL)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Zero(t, callbackError(t, rec), rec.Body.String())

	require.Equal(t, 2, h.versionCount(t), "status 2 must commit a new version")
	headID, headNum, headSHA := h.headVersion(t)
	require.Equal(t, 2, headNum)
	sum := sha256.Sum256(edited)
	require.Equal(t, hex.EncodeToString(sum[:]), headSHA)
	require.Equal(t, 1, h.uploadedEventCount(t, headID),
		"OnlyOffice save must emit dms.version.uploaded.v1")
}

func TestOnlyOffice_Status6_ForceSaveSavesNewVersion(t *testing.T) {
	h := newOfficeHarness(t)
	edited := []byte("EDITED in onlyoffice — force save content")
	mux, path, jwtSecret, dlURL := onlyOfficeSetup(t, h, edited)

	rec := postCallback(t, mux, path, jwtSecret, 6, dlURL)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Zero(t, callbackError(t, rec), rec.Body.String())
	require.Equal(t, 2, h.versionCount(t), "status 6 (force save) must commit a new version")
	var summary string
	require.NoError(t, h.pool.QueryRow(h.ctx, `
		SELECT change_summary FROM document_versions
		WHERE tenant_id=$1 AND document_id=$2 AND version_number=2`,
		h.tenant, h.docID).Scan(&summary))
	require.Contains(t, summary, "force save")
}

func TestOnlyOffice_Status4_NoChanges_NoNewVersion(t *testing.T) {
	h := newOfficeHarness(t)
	mux, path, jwtSecret, dlURL := onlyOfficeSetup(t, h, []byte("unused"))

	rec := postCallback(t, mux, path, jwtSecret, 4, dlURL)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Zero(t, callbackError(t, rec))
	require.Equal(t, 1, h.versionCount(t), "status 4 (closed unchanged) must not write")
}

func TestOnlyOffice_BadJWT_Rejected(t *testing.T) {
	h := newOfficeHarness(t)
	mux, path, _, dlURL := onlyOfficeSetup(t, h, []byte("attacker bytes"))

	// Signed with the WRONG secret → 403, nothing saved.
	rec := postCallback(t, mux, path, "not-the-real-secret", 2, dlURL)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Equal(t, 1, callbackError(t, rec))
	require.Equal(t, 1, h.versionCount(t), "forged callback must not write")
}

func TestOnlyOffice_MissingAccessToken_Rejected(t *testing.T) {
	h := newOfficeHarness(t)
	edited := []byte("edited without access token")
	mux, _, jwtSecret, dlURL := onlyOfficeSetup(t, h, edited)

	// Valid DS JWT but NO access_token on the callback URL — the save
	// path has no caller identity and must refuse.
	bare := "/api/v1/documents/" + h.docID.String() + "/versions/" + h.v1.String() + "/onlyoffice/callback"
	rec := postCallback(t, mux, bare, jwtSecret, 2, dlURL)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 1, callbackError(t, rec), "missing access_token must fail the save")
	require.Equal(t, 1, h.versionCount(t))
}

func TestOnlyOffice_WOPILockHeld_RejectsSaveBack(t *testing.T) {
	h := newOfficeHarness(t)
	edited := []byte("edit racing a collabora session")
	mux, path, jwtSecret, dlURL := onlyOfficeSetup(t, h, edited)

	// Another editor session holds the WOPI lock on this version.
	h.mr.Set(wopiLockKey(h.v1), "collabora-session-9")

	rec := postCallback(t, mux, path, jwtSecret, 2, dlURL)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 1, callbackError(t, rec), "locked version must reject non-holder save-back")
	require.Equal(t, 1, h.versionCount(t))
}

func TestOnlyOffice_SSRFHostGuard_RejectsForeignURL(t *testing.T) {
	h := newOfficeHarness(t)
	mux, path, jwtSecret, _ := onlyOfficeSetup(t, h, []byte("unused"))

	// A save event pointing anywhere but the configured DS is refused.
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("evil bytes"))
	}))
	t.Cleanup(evil.Close)

	rec := postCallback(t, mux, path, jwtSecret, 2, evil.URL+"/f.docx")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 1, callbackError(t, rec), "download from a non-DS host must be refused")
	require.Equal(t, 1, h.versionCount(t))
}
