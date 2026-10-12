// OnlyOffice editor surface — security regressions (D-5, D-6, D-11).
//
// Unit tests: miniredis for the WOPI lock store, a recording saver in place
// of the DB/S3 save core, and a fake coauth authorizer in place of the
// policy-backed DocumentService.
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/auth"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
)

const ooTestSecret = "onlyoffice-unit-secret"

// recordingSaver stands in for DBWOPIResolver.SaveFromURL.
type recordingSaver struct {
	mu    sync.Mutex
	calls []WOPIClaims
}

func (s *recordingSaver) SaveFromURL(_ context.Context, c *WOPIClaims, _, _, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, *c)
	return nil
}

func (s *recordingSaver) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// ooEvent signs a Document-Server-style callback body (body-form token) the
// way the DS does: the event plus iat/exp claims.
func ooEvent(t *testing.T, status int, key string, iat, exp time.Time) []byte {
	t.Helper()
	ev := map[string]any{"status": status, "key": key, "url": "http://ds.test/cache/edited.docx"}
	if !iat.IsZero() {
		ev["iat"] = iat.Unix()
	}
	if !exp.IsZero() {
		ev["exp"] = exp.Unix()
	}
	tok, err := hs256(ev, ooTestSecret)
	if err != nil {
		t.Fatalf("hs256: %v", err)
	}
	ev["token"] = tok
	b, _ := json.Marshal(ev)
	return b
}

func ooPost(mux *http.ServeMux, docID, verID uuid.UUID, accessToken string, body []byte) *httptest.ResponseRecorder {
	path := "/api/v1/documents/" + docID.String() + "/versions/" + verID.String() +
		"/onlyoffice/callback?access_token=" + url.QueryEscape(accessToken)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func ooErrorField(t *testing.T, rec *httptest.ResponseRecorder) int {
	t.Helper()
	var out struct {
		Error int `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("callback body %q: %v", rec.Body.String(), err)
	}
	return out.Error
}

func newOOTestRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return mr, rdb
}

// D-5 — WOPI keys its single-writer lock on the DOCUMENT (a session keeps
// one token while every save moves the head to a new version), but the
// OnlyOffice save-back looked the lock up under the VERSION id. An
// OnlyOffice editor therefore never saw a Collabora/WOPI lock and the two
// overwrote each other.
func TestOnlyOfficeSave_RespectsWOPILock(t *testing.T) {
	t.Setenv("SEDOC_ONLYOFFICE_JWT", ooTestSecret)
	t.Setenv("SEDOC_ONLYOFFICE_URL", "http://ds.test")
	t.Setenv("SEDOC_WOPI_SECRET", "wopi-unit-secret")
	_, rdb := newOOTestRedis(t)

	tenant, user, doc := uuid.New(), uuid.New(), uuid.New()
	v1, v2 := uuid.New(), uuid.New() // v2: the head after a WOPI save

	// A WOPI (Collabora) session opened v1 and holds the document lock,
	// taken through the real WOPI handler.
	wopi := NewWOPIHandler(rdb, zerolog.Nop())
	wopiMux := http.NewServeMux()
	wopi.Register(wopiMux)
	wopiTok := IssueWOPIToken("wopi-unit-secret", WOPIClaims{
		TenantID: tenant, UserID: uuid.New(), FileID: v1, DocumentID: doc,
		ExpiresAt: time.Now().Add(time.Hour), CanWrite: true,
	})
	lockReq := httptest.NewRequest(http.MethodPost, "/wopi/files/"+v1.String()+"?access_token="+url.QueryEscape(wopiTok), nil)
	lockReq.Header.Set("X-WOPI-Override", "LOCK")
	lockReq.Header.Set("X-WOPI-Lock", "collabora-session-1")
	lockRec := httptest.NewRecorder()
	wopiMux.ServeHTTP(lockRec, lockReq)
	if lockRec.Code != http.StatusOK {
		t.Fatalf("WOPI lock: %d %s", lockRec.Code, lockRec.Body.String())
	}

	saver := &recordingSaver{}
	oo := NewOnlyOfficeHandler(zerolog.Nop())
	oo.Saver = saver
	oo.Redis = rdb
	mux := http.NewServeMux()
	oo.Register(mux)

	now := time.Now()
	for _, ver := range []uuid.UUID{v1, v2} {
		access := IssueWOPIToken(ooTestSecret, WOPIClaims{
			TenantID: tenant, UserID: user, FileID: ver, DocumentID: doc,
			ExpiresAt: now.Add(time.Hour), CanWrite: true,
		})
		rec := ooPost(mux, doc, ver, access, ooEvent(t, 2, ver.String(), now, now.Add(5*time.Minute)))
		if got := ooErrorField(t, rec); got != 1 {
			t.Fatalf("save-back on %s while the document is WOPI-locked: want error 1, got %d", ver, got)
		}
	}
	if saver.count() != 0 {
		t.Fatalf("locked document must not be saved by OnlyOffice; saver called %d times", saver.count())
	}

	// Lock released → the same save-back goes through.
	if err := rdb.Del(context.Background(), wopiLockKey(doc)).Err(); err != nil {
		t.Fatal(err)
	}
	access := IssueWOPIToken(ooTestSecret, WOPIClaims{
		TenantID: tenant, UserID: user, FileID: v2, DocumentID: doc,
		ExpiresAt: now.Add(time.Hour), CanWrite: true,
	})
	rec := ooPost(mux, doc, v2, access, ooEvent(t, 2, v2.String(), now, now.Add(5*time.Minute)))
	if got := ooErrorField(t, rec); got != 0 {
		t.Fatalf("unlocked save-back: want error 0, got %d (%s)", got, rec.Body.String())
	}
	if saver.count() != 1 {
		t.Fatalf("unlocked save-back: saver calls = %d, want 1", saver.count())
	}
}

// fakeCoauthAuthz stands in for DocumentService.AuthorizeCoauth.
type fakeCoauthAuthz struct {
	docID    uuid.UUID
	canWrite bool
	err      error
	calls    int
}

func (f *fakeCoauthAuthz) AuthorizeCoauth(_ context.Context, _ uuid.UUID) (uuid.UUID, bool, error) {
	f.calls++
	return f.docID, f.canWrite, f.err
}

func ooConfig(mux *http.ServeMux, tenant, user, doc, ver uuid.UUID) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/documents/"+doc.String()+"/versions/"+ver.String()+"/onlyoffice/config", nil)
	req = ztAsRole(req, tenant, user, "member")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// D-6 — the config endpoint minted a signed edit config with a 24 h
// write-capable callback token for ANY version id, with no authorization:
// any tenant member could obtain a save-back credential for a document they
// cannot even read. Config now authorizes through AuthorizeCoauth, requires
// edit, binds the token to the version's document, and caps it at 1 h.
func TestOnlyOfficeConfig_RequiresEdit(t *testing.T) {
	t.Setenv("SEDOC_ONLYOFFICE_JWT", ooTestSecret)
	tenant, user, doc, ver := uuid.New(), uuid.New(), uuid.New(), uuid.New()

	run := func(authz *fakeCoauthAuthz, pathDoc uuid.UUID) *httptest.ResponseRecorder {
		oo := NewOnlyOfficeHandler(zerolog.Nop())
		oo.Authz = authz
		mux := http.NewServeMux()
		oo.Register(mux)
		return ooConfig(mux, tenant, user, pathDoc, ver)
	}

	// Cannot view the document → 404, no config.
	rec := run(&fakeCoauthAuthz{err: errNotFoundForTest()}, doc)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unviewable version: want 404, got %d %s", rec.Code, rec.Body.String())
	}
	// View-only → 403, no write token.
	rec = run(&fakeCoauthAuthz{docID: doc, canWrite: false}, doc)
	if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "access_token") {
		t.Fatalf("view-only caller: want 403 without a token, got %d %s", rec.Code, rec.Body.String())
	}
	// Version of a DIFFERENT document under this document's path → 404.
	rec = run(&fakeCoauthAuthz{docID: uuid.New(), canWrite: true}, doc)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("version from another document: want 404, got %d %s", rec.Code, rec.Body.String())
	}

	// Editor → 200; the callback token is bound to (version, document) and
	// expires within an hour.
	rec = run(&fakeCoauthAuthz{docID: doc, canWrite: true}, doc)
	if rec.Code != http.StatusOK {
		t.Fatalf("editor: want 200, got %d %s", rec.Code, rec.Body.String())
	}
	var cfg onlyOfficeConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatal(err)
	}
	cb, err := url.Parse(cfg.EditorConfig.CallbackURL)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := parseWOPIToken(ooTestSecret, cb.Query().Get("access_token"))
	if err != nil {
		t.Fatalf("callback access_token: %v", err)
	}
	if claims.FileID != ver || claims.DocumentID != doc || claims.UserID != user || claims.TenantID != tenant {
		t.Fatalf("callback token claims not bound to the caller/version/document: %+v", claims)
	}
	if !claims.CanWrite {
		t.Fatal("editor token must be write-capable")
	}
	if ttl := time.Until(claims.ExpiresAt); ttl > time.Hour+time.Minute {
		t.Fatalf("callback token TTL %s exceeds 1h", ttl)
	}
}

// Fail closed when the authorizer is not wired.
func TestOnlyOfficeConfig_NoAuthorizer503(t *testing.T) {
	t.Setenv("SEDOC_ONLYOFFICE_JWT", ooTestSecret)
	oo := NewOnlyOfficeHandler(zerolog.Nop())
	mux := http.NewServeMux()
	oo.Register(mux)
	rec := ooConfig(mux, uuid.New(), uuid.New(), uuid.New(), uuid.New())
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 without an authorizer, got %d", rec.Code)
	}
}

func ztAsRole(req *http.Request, tenant, user uuid.UUID, role string) *http.Request {
	return req.WithContext(auth.WithUser(auth.SetTenantID(req.Context(), tenant),
		auth.UserInfo{ID: user, TenantID: tenant, Role: role}))
}

func errNotFoundForTest() error { return vdmserr.ErrNotFound }
