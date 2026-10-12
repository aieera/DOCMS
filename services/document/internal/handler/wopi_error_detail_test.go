// D-12 follow-up — WOPI error bodies must not echo internal error text
// (e.g. "resolve: load wopi caller groups: wopi: user is not active", SQL
// or storage errors) to the editor/client. Generic body, detail in the log.
package handler

import (
	"context"
	"errors"
	"fmt"
	stdio "io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

type failingResolver struct{ err error }

func (f failingResolver) Resolve(*http.Request, *WOPIClaims) (*WOPIDoc, error) { return nil, f.err }
func (f failingResolver) Open(*http.Request, *WOPIClaims) (stdio.ReadCloser, int64, error) {
	return nil, 0, f.err
}
func (f failingResolver) Save(*http.Request, *WOPIClaims, stdio.Reader) error { return f.err }
func (f failingResolver) SaveAs(*http.Request, *WOPIClaims, string, string, stdio.Reader) (string, string, string, error) {
	return "", "", "", f.err
}

func TestWOPIErrors_NoInternalDetail(t *testing.T) {
	t.Setenv("SEDOC_WOPI_SECRET", "wopi-err-secret")
	_, rdb := newOOTestRedis(t)
	ver := uuid.New()
	tok := url.QueryEscape(IssueWOPIToken("wopi-err-secret", WOPIClaims{
		TenantID: uuid.New(), UserID: uuid.New(), FileID: ver, DocumentID: uuid.New(),
		ExpiresAt: time.Now().Add(time.Hour), CanWrite: true,
	}))
	for _, cause := range []error{
		fmt.Errorf("load wopi caller groups: %w", errWOPIUserInactive),
		errors.New(`database error: relation "document_versions" secret-detail`),
	} {
		wh := NewWOPIHandler(rdb, zerolog.Nop())
		wh.FileResolver = failingResolver{err: cause}
		mux := http.NewServeMux()
		wh.Register(mux)
		for _, req := range []*http.Request{
			httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/wopi/files/"+ver.String()+"?access_token="+tok, nil),
			httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/wopi/files/"+ver.String()+"/contents?access_token="+tok, nil),
			httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/wopi/files/"+ver.String()+"/contents?access_token="+tok, strings.NewReader("x")),
		} {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			body := rec.Body.String()
			if rec.Code < 400 || strings.Contains(body, "not active") || strings.Contains(body, "secret-detail") ||
				strings.Contains(body, "wopi:") || strings.Contains(body, "database") {
				t.Fatalf("%s %s: status %d leaks internal detail: %q", req.Method, req.URL.Path, rec.Code, body)
			}
		}
	}
}
