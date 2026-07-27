// ADR 0065 — lock-semantics tests against an in-memory Redis.
//
// Pins the §10.3 invariant "lock prevents out-of-band edits":
//   - Editor A acquires a lock; editor B (different X-WOPI-Lock value)
//     gets 409 + the existing lock returned in X-WOPI-Lock.
//   - Editor A's PutFile with the matching lock value succeeds.
//   - Editor B's PutFile with a different lock value gets 409.
//   - Refresh extends the TTL only when the value matches.
//   - Unlock requires the value to match.
//
// Uses miniredis so no external Redis container is needed; the
// production path is the same go-redis client API.
package handler

import (
	"context"
	stdio "io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
)

// fakeResolver is a minimal WOPIFileResolver that lets PutFile reach
// the Save path without needing storage / policy plumbing.
type fakeResolver struct {
	saved []byte
}

func (f *fakeResolver) Resolve(_ *http.Request, _ *WOPIClaims) (*WOPIDoc, error) {
	return &WOPIDoc{BaseFileName: "x.docx", UserCanWrite: true}, nil
}
func (f *fakeResolver) Open(_ *http.Request, _ *WOPIClaims) (rc stdio.ReadCloser, size int64, err error) {
	return nil, 0, nil
}
func (f *fakeResolver) Save(_ *http.Request, _ *WOPIClaims, body stdio.Reader) error {
	b, _ := stdio.ReadAll(body)
	f.saved = b
	return nil
}
func (f *fakeResolver) SaveAs(_ *http.Request, _ *WOPIClaims, _, _ string, _ stdio.Reader) (string, string, string, error) {
	return "", "", "", nil
}

func newWOPIHarness(t *testing.T) (*WOPIHandler, *miniredis.Miniredis, *WOPIClaims, string, func()) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	secret := "test-wopi-secret"
	t.Setenv("SEDOC_WOPI_SECRET", secret)

	c := &WOPIClaims{
		TenantID:  uuid.New(),
		UserID:    uuid.New(),
		FileID:    uuid.New(),
		ExpiresAt: time.Now().Add(time.Hour),
		CanWrite:  true,
	}
	tok := IssueWOPIToken(secret, *c)

	h := NewWOPIHandler(rdb, zerolog.Nop())
	h.FileResolver = &fakeResolver{}

	cleanup := func() {
		_ = rdb.Close()
		mr.Close()
	}
	return h, mr, c, tok, cleanup
}

// makeReq builds a /wopi/files/{file_id} request with the lock + token.
func makeReq(method, op, lockVal, tok string, c *WOPIClaims, body string) *http.Request {
	url := "/wopi/files/" + c.FileID.String()
	if strings.Contains(method, "PUT") || op == "" {
		// PutFile uses /contents
		if method == "POST_PUTFILE" {
			method = "POST"
			url += "/contents"
		}
	}
	url += "?access_token=" + tok
	r := httptest.NewRequest(method, url, strings.NewReader(body))
	r.SetPathValue("file_id", c.FileID.String())
	if op != "" {
		r.Header.Set("X-WOPI-Override", op)
	}
	if lockVal != "" {
		r.Header.Set("X-WOPI-Lock", lockVal)
	}
	return r
}

func TestLock_AcquireAndConflict(t *testing.T) {
	h, _, c, tok, done := newWOPIHarness(t)
	defer done()

	// Editor A acquires the lock.
	w := httptest.NewRecorder()
	h.fileOperation(w, makeReq("POST", "LOCK", "editor-A-lock", tok, c, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("editor A lock: got %d, body=%q", w.Code, w.Body.String())
	}

	// Editor B tries to acquire with a DIFFERENT lock value.
	w = httptest.NewRecorder()
	h.fileOperation(w, makeReq("POST", "LOCK", "editor-B-lock", tok, c, ""))
	if w.Code != http.StatusConflict {
		t.Errorf("editor B lock: got %d, want 409", w.Code)
	}
	if got := w.Header().Get("X-WOPI-Lock"); got != "editor-A-lock" {
		t.Errorf("conflict response must echo existing lock; got %q", got)
	}
}

func TestLock_PutFile_RequiresMatchingLock(t *testing.T) {
	h, _, c, tok, done := newWOPIHarness(t)
	defer done()

	// Editor A locks, then PutFile with the matching lock — must succeed.
	rec := httptest.NewRecorder()
	h.fileOperation(rec, makeReq("POST", "LOCK", "matching-lock", tok, c, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("lock failed: %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.putFile(rec, makeReq("POST_PUTFILE", "", "matching-lock", tok, c, "edited content"))
	if rec.Code != http.StatusOK {
		t.Errorf("PutFile with matching lock: got %d, body=%q", rec.Code, rec.Body.String())
	}
	if string(h.FileResolver.(*fakeResolver).saved) != "edited content" {
		t.Errorf("FileResolver.Save did not receive body")
	}

	// Editor B's PutFile with a DIFFERENT lock value → 409.
	rec = httptest.NewRecorder()
	h.putFile(rec, makeReq("POST_PUTFILE", "", "different-lock", tok, c, "out of band write"))
	if rec.Code != http.StatusConflict {
		t.Errorf("PutFile with mismatched lock: got %d, want 409 (out-of-band edits must be blocked)", rec.Code)
	}
	if got := rec.Header().Get("X-WOPI-Lock"); got != "matching-lock" {
		t.Errorf("conflict echo: got %q want %q", got, "matching-lock")
	}
}

func TestLock_RefreshOnlyWhenValueMatches(t *testing.T) {
	h, mr, c, tok, done := newWOPIHarness(t)
	defer done()

	// Lock + manually shrink the TTL so we can detect a refresh.
	w := httptest.NewRecorder()
	h.fileOperation(w, makeReq("POST", "LOCK", "lock-X", tok, c, ""))
	mr.SetTTL(wopiLockKey(c.FileID), 1*time.Minute)

	// Refresh with the right value extends the TTL back to 30 min.
	w = httptest.NewRecorder()
	h.fileOperation(w, makeReq("POST", "REFRESH_LOCK", "lock-X", tok, c, ""))
	if w.Code != http.StatusOK {
		t.Errorf("refresh with matching value: %d", w.Code)
	}
	if ttl := mr.TTL(wopiLockKey(c.FileID)); ttl < 25*time.Minute {
		t.Errorf("refresh did not extend TTL; got %v", ttl)
	}

	// Refresh with the WRONG value → 409, TTL unchanged.
	mr.SetTTL(wopiLockKey(c.FileID), 1*time.Minute)
	w = httptest.NewRecorder()
	h.fileOperation(w, makeReq("POST", "REFRESH_LOCK", "wrong-value", tok, c, ""))
	if w.Code != http.StatusConflict {
		t.Errorf("refresh with mismatched value: got %d, want 409", w.Code)
	}
}

func TestLock_UnlockRequiresMatchingValue(t *testing.T) {
	h, _, c, tok, done := newWOPIHarness(t)
	defer done()

	w := httptest.NewRecorder()
	h.fileOperation(w, makeReq("POST", "LOCK", "lock-Y", tok, c, ""))

	// Unlock with the wrong value → 409.
	w = httptest.NewRecorder()
	h.fileOperation(w, makeReq("POST", "UNLOCK", "wrong", tok, c, ""))
	if w.Code != http.StatusConflict {
		t.Errorf("unlock with wrong value: got %d, want 409", w.Code)
	}

	// Unlock with the right value → 200, key deleted.
	w = httptest.NewRecorder()
	h.fileOperation(w, makeReq("POST", "UNLOCK", "lock-Y", tok, c, ""))
	if w.Code != http.StatusOK {
		t.Errorf("unlock with matching value: %d", w.Code)
	}

	// Now lockable again by anyone.
	w = httptest.NewRecorder()
	h.fileOperation(w, makeReq("POST", "LOCK", "fresh-lock", tok, c, ""))
	if w.Code != http.StatusOK {
		t.Errorf("re-lock after unlock: %d", w.Code)
	}
}

// readOnlyToken mints a WOPI token for the same file as `c` but with
// CanWrite=false, using the secret the harness installed in the env.
func readOnlyToken(t *testing.T, c *WOPIClaims) string {
	t.Helper()
	ro := *c
	ro.CanWrite = false
	return IssueWOPIToken(os.Getenv("SEDOC_WOPI_SECRET"), ro)
}

// TestLock_ReadOnlyTokenCannotTouchLock pins the fix for the read-only
// lock-manipulation gap: GetLock / Unlock / RefreshLock must reject a
// read-only token, so it can neither learn nor destroy an editor's lock.
func TestLock_ReadOnlyTokenCannotTouchLock(t *testing.T) {
	h, _, c, tok, done := newWOPIHarness(t)
	defer done()

	// Editor A (write token) holds the lock.
	w := httptest.NewRecorder()
	h.fileOperation(w, makeReq("POST", "LOCK", "editor-A-lock", tok, c, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("editor A lock: %d", w.Code)
	}

	roTok := readOnlyToken(t, c)

	// GET_LOCK with a read-only token must NOT leak the lock value.
	w = httptest.NewRecorder()
	h.fileOperation(w, makeReq("POST", "GET_LOCK", "", roTok, c, ""))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("read-only GET_LOCK: got %d, want 401", w.Code)
	}
	if got := w.Header().Get("X-WOPI-Lock"); got == "editor-A-lock" {
		t.Errorf("read-only GET_LOCK leaked the lock value %q", got)
	}

	// UNLOCK with a read-only token (even with the correct value) must fail.
	w = httptest.NewRecorder()
	h.fileOperation(w, makeReq("POST", "UNLOCK", "editor-A-lock", roTok, c, ""))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("read-only UNLOCK: got %d, want 401", w.Code)
	}

	// The lock must still be intact: editor A can refresh it.
	w = httptest.NewRecorder()
	h.fileOperation(w, makeReq("POST", "REFRESH_LOCK", "editor-A-lock", tok, c, ""))
	if w.Code != http.StatusOK {
		t.Errorf("editor A lock was destroyed by the read-only token: refresh got %d", w.Code)
	}
}

// TestLock_UnlockAndRelock pins the fix for X-WOPI-OldLock handling: a
// LOCK carrying OldLock atomically rotates the lock when OldLock matches,
// and 409s (echoing the current lock) when it doesn't.
func TestLock_UnlockAndRelock(t *testing.T) {
	h, _, c, tok, done := newWOPIHarness(t)
	defer done()

	w := httptest.NewRecorder()
	h.fileOperation(w, makeReq("POST", "LOCK", "lock-1", tok, c, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("initial lock: %d", w.Code)
	}

	// Valid rotation: OldLock == current → replace with lock-2.
	r := makeReq("POST", "LOCK", "lock-2", tok, c, "")
	r.Header.Set("X-WOPI-OldLock", "lock-1")
	w = httptest.NewRecorder()
	h.fileOperation(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("UnlockAndRelock with correct OldLock: got %d, want 200, body=%q", w.Code, w.Body.String())
	}

	// A plain LOCK with the OLD value must now conflict (lock is lock-2).
	w = httptest.NewRecorder()
	h.fileOperation(w, makeReq("POST", "LOCK", "lock-1", tok, c, ""))
	if w.Code != http.StatusConflict || w.Header().Get("X-WOPI-Lock") != "lock-2" {
		t.Errorf("after rotation, current lock should be lock-2; got code=%d hdr=%q", w.Code, w.Header().Get("X-WOPI-Lock"))
	}

	// Rotation with a WRONG OldLock → 409 echoing the current lock.
	r = makeReq("POST", "LOCK", "lock-3", tok, c, "")
	r.Header.Set("X-WOPI-OldLock", "not-the-current-lock")
	w = httptest.NewRecorder()
	h.fileOperation(w, r)
	if w.Code != http.StatusConflict {
		t.Errorf("UnlockAndRelock with wrong OldLock: got %d, want 409", w.Code)
	}
	if got := w.Header().Get("X-WOPI-Lock"); got != "lock-2" {
		t.Errorf("conflict must echo current lock lock-2; got %q", got)
	}
}

// recordingAuditor satisfies WOPIAuditor and remembers every call.
type recordingAuditor struct {
	started []*WOPIClaims
	ended   []struct {
		c        *WOPIClaims
		duration int64
	}
}

func (r *recordingAuditor) SessionStarted(_ context.Context, c *WOPIClaims) {
	r.started = append(r.started, c)
}
func (r *recordingAuditor) SessionEnded(_ context.Context, c *WOPIClaims, d int64) {
	r.ended = append(r.ended, struct {
		c        *WOPIClaims
		duration int64
	}{c, d})
}

func TestAudit_FiresOnLockAndUnlock(t *testing.T) {
	h, _, c, tok, done := newWOPIHarness(t)
	defer done()
	rec := &recordingAuditor{}
	h.Auditor = rec

	// Lock = session start.
	w := httptest.NewRecorder()
	h.fileOperation(w, makeReq("POST", "LOCK", "lock", tok, c, ""))
	if len(rec.started) != 1 {
		t.Fatalf("session_started not emitted on lock; got %d", len(rec.started))
	}
	if rec.started[0].UserID != c.UserID {
		t.Errorf("audit user id mismatch: %v vs %v", rec.started[0].UserID, c.UserID)
	}

	// Unlock = session end.
	w = httptest.NewRecorder()
	h.fileOperation(w, makeReq("POST", "UNLOCK", "lock", tok, c, ""))
	if len(rec.ended) != 1 {
		t.Fatalf("session_ended not emitted on unlock; got %d", len(rec.ended))
	}
	if rec.ended[0].c.UserID != c.UserID {
		t.Errorf("audit end user id mismatch")
	}
}

func TestAudit_TwoUsers_BothTracked(t *testing.T) {
	// Pins §10.3 "audit trails both editors". Real WOPI flow: each
	// editor opens with CheckFileInfo (which triggers
	// session_started); then each tries to Lock. Only one Lock
	// succeeds — but BOTH users have an audit row from their
	// CheckFileInfo call, which is the right semantic ("user X
	// opened the document" is auditable independently of who owns
	// the write lock).
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	secret := "test-secret"
	t.Setenv("SEDOC_WOPI_SECRET", secret)

	tenantID := uuid.New()
	fileID := uuid.New()
	userA := uuid.New()
	userB := uuid.New()

	rec := &recordingAuditor{}
	h := NewWOPIHandler(rdb, zerolog.Nop())
	h.Auditor = rec
	h.FileResolver = &fakeResolver{}

	// Each user opens the document — the WOPI editor's first call
	// is always CheckFileInfo. That triggers session_started.
	for _, u := range []uuid.UUID{userA, userB} {
		c := WOPIClaims{
			TenantID: tenantID, UserID: u, FileID: fileID,
			ExpiresAt: time.Now().Add(time.Hour), CanWrite: true,
		}
		tok := IssueWOPIToken(secret, c)
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/wopi/files/"+fileID.String()+"?access_token="+tok, nil)
		r.SetPathValue("file_id", fileID.String())
		h.checkFileInfo(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("checkFileInfo for user %v: %d", u, w.Code)
		}
	}

	if len(rec.started) != 2 {
		t.Fatalf("expected 2 session_started events (one per user open); got %d", len(rec.started))
	}
	users := map[uuid.UUID]bool{}
	for _, s := range rec.started {
		users[s.UserID] = true
	}
	if !users[userA] || !users[userB] {
		t.Errorf("audit must record BOTH editors; got %v", users)
	}

	_ = context.Background() // keep import resolved
}
