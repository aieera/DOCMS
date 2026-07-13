// WOPI PutFile lock enforcement — the non-holder rejection invariant.
//
// The old putFile only compared locks when the caller SUPPLIED an
// X-WOPI-Lock header, so a PutFile with no header sailed straight past
// an existing lock: any writer could overwrite a file another editor
// session had locked. These tests pin the fixed shape with miniredis
// (no container needed):
//
//   - locked file + missing header   → 409 + current lock echoed
//   - locked file + mismatched header → 409 + current lock echoed
//   - locked file + matching header   → save proceeds
//   - unlocked file + no header       → save proceeds
//   - unlocked file + stale header    → 409 "no lock"
package handler

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
)

// recordingResolver satisfies WOPIFileResolver; Save records the call.
type recordingResolver struct {
	saved   int
	saveErr error
}

func (f *recordingResolver) Resolve(*http.Request, *WOPIClaims) (*WOPIDoc, error) {
	return &WOPIDoc{BaseFileName: "t.docx", Size: 1, Version: "1", UserCanWrite: true}, nil
}
func (f *recordingResolver) Open(*http.Request, *WOPIClaims) (io.ReadCloser, int64, error) {
	return io.NopCloser(strings.NewReader("x")), 1, nil
}
func (f *recordingResolver) Save(_ *http.Request, _ *WOPIClaims, body io.Reader) error {
	_, _ = io.Copy(io.Discard, body)
	f.saved++
	return f.saveErr
}
func (f *recordingResolver) SaveAs(*http.Request, *WOPIClaims, string, string, io.Reader) (string, string, string, error) {
	return "", "", "", errors.New("unused")
}

func newWOPILockHarness(t *testing.T) (*WOPIHandler, *recordingResolver, *miniredis.Miniredis, WOPIClaims, string) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	const secret = "putfile-lock-test-secret"
	t.Setenv("SEDOC_WOPI_SECRET", secret)

	res := &recordingResolver{}
	h := NewWOPIHandler(rdb, zerolog.Nop())
	h.FileResolver = res

	claims := WOPIClaims{
		TenantID:  uuid.New(),
		UserID:    uuid.New(),
		FileID:    uuid.New(),
		ExpiresAt: time.Now().Add(time.Hour),
		CanWrite:  true,
	}
	return h, res, mr, claims, IssueWOPIToken(secret, claims)
}

func doPutFile(t *testing.T, h *WOPIHandler, claims WOPIClaims, token, lockHeader string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	h.Register(mux)
	req := httptest.NewRequest(http.MethodPost,
		"/wopi/files/"+claims.FileID.String()+"/contents?access_token="+token,
		strings.NewReader("edited-bytes"))
	if lockHeader != "" {
		req.Header.Set("X-WOPI-Lock", lockHeader)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestPutFile_LockedFile_MissingHeaderRejected(t *testing.T) {
	h, res, mr, claims, token := newWOPILockHarness(t)
	mr.Set(wopiLockKey(claims.FileID), "holder-lock-id")

	rec := doPutFile(t, h, claims, token, "") // non-holder: no lock header
	if rec.Code != http.StatusConflict {
		t.Fatalf("code=%d want 409 (locked file must reject un-locked PutFile)", rec.Code)
	}
	if got := rec.Header().Get("X-WOPI-Lock"); got != "holder-lock-id" {
		t.Errorf("X-WOPI-Lock=%q want current holder's lock echoed", got)
	}
	if res.saved != 0 {
		t.Errorf("Save called %d times; a rejected PutFile must not write", res.saved)
	}
}

func TestPutFile_LockedFile_MismatchedHeaderRejected(t *testing.T) {
	h, res, mr, claims, token := newWOPILockHarness(t)
	mr.Set(wopiLockKey(claims.FileID), "holder-lock-id")

	rec := doPutFile(t, h, claims, token, "someone-elses-lock")
	if rec.Code != http.StatusConflict {
		t.Fatalf("code=%d want 409", rec.Code)
	}
	if got := rec.Header().Get("X-WOPI-Lock"); got != "holder-lock-id" {
		t.Errorf("X-WOPI-Lock=%q want holder's lock", got)
	}
	if res.saved != 0 {
		t.Errorf("Save called %d times; want 0", res.saved)
	}
}

func TestPutFile_LockedFile_HolderSaves(t *testing.T) {
	h, res, mr, claims, token := newWOPILockHarness(t)
	mr.Set(wopiLockKey(claims.FileID), "holder-lock-id")

	rec := doPutFile(t, h, claims, token, "holder-lock-id")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200 (holder must be able to save): %s", rec.Code, rec.Body.String())
	}
	if res.saved != 1 {
		t.Errorf("Save called %d times; want 1", res.saved)
	}
}

func TestPutFile_UnlockedFile_NoHeaderSaves(t *testing.T) {
	h, res, _, claims, token := newWOPILockHarness(t)

	rec := doPutFile(t, h, claims, token, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200: %s", rec.Code, rec.Body.String())
	}
	if res.saved != 1 {
		t.Errorf("Save called %d times; want 1", res.saved)
	}
}

func TestPutFile_UnlockedFile_StaleHeaderRejected(t *testing.T) {
	h, res, _, claims, token := newWOPILockHarness(t)

	rec := doPutFile(t, h, claims, token, "stale-lock")
	if rec.Code != http.StatusConflict {
		t.Fatalf("code=%d want 409 (stale lock header on unlocked file)", rec.Code)
	}
	if res.saved != 0 {
		t.Errorf("Save called %d times; want 0", res.saved)
	}
}
