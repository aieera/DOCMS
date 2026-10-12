package handler

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/services/mcp-server/internal/mcp"
)

// fakeKeyAuth stands in for APIKeyAuth: the test names the caller's tenant
// and user in headers, and this stamps them on ctx exactly as APIKeyAuth does.
func fakeKeyAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tid, _ := uuid.Parse(r.Header.Get("T-Tenant"))
		uid, _ := uuid.Parse(r.Header.Get("T-User"))
		ctx := auth.WithUser(r.Context(), auth.UserInfo{ID: uid, TenantID: tid, Role: "api_key"})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type ident struct{ tenant, user uuid.UUID }

func newIdent() ident { return ident{uuid.New(), uuid.New()} }

func (i ident) set(r *http.Request) {
	r.Header.Set("T-Tenant", i.tenant.String())
	r.Header.Set("T-User", i.user.String())
}

// openSSE opens the stream as id and returns the POST endpoint plus a channel
// of `data:` payloads from subsequent `event: message` frames.
func openSSE(t *testing.T, ctx context.Context, base string, id ident) (string, <-chan string) {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/mcp/sse", nil)
	id.set(req)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, http.StatusOK, resp.StatusCode)
	sc := bufio.NewScanner(resp.Body)
	var endpoint string
	for sc.Scan() {
		if line := sc.Text(); strings.HasPrefix(line, "data: ") {
			endpoint = strings.TrimPrefix(line, "data: ")
			break
		}
	}
	require.NotEmpty(t, endpoint)
	msgs := make(chan string, 8)
	go func() {
		event := ""
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: ") && event == "message":
				msgs <- strings.TrimPrefix(line, "data: ")
			}
		}
		close(msgs)
	}()
	return endpoint, msgs
}

func postFrame(t *testing.T, base, endpoint string, id ident, body string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, base+endpoint, strings.NewReader(body))
	id.set(req)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	return resp.StatusCode
}

// R-6: a session is bound to the identity that opened it. Another key (other
// tenant, or same tenant other user) that learns the session_id must not be
// able to inject responses into that stream.
func TestSSE_ForeignKeyCannotPost(t *testing.T) {
	h := New(mcp.New(mcp.Config{Logger: zerolog.Nop()}), zerolog.Nop())
	mux := http.NewServeMux()
	h.Register(mux)
	ts := httptest.NewServer(fakeKeyAuth(mux))
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	owner := newIdent()
	endpoint, msgs := openSSE(t, ctx, ts.URL, owner)

	initFrame := `{"jsonrpc":"2.0","id":1,"method":"initialize"}`
	otherTenant := newIdent()
	sameTenantOtherUser := ident{tenant: owner.tenant, user: uuid.New()}
	for name, id := range map[string]ident{"other tenant": otherTenant, "same tenant, other user": sameTenantOtherUser} {
		code := postFrame(t, ts.URL, endpoint, id, initFrame)
		assert.NotEqual(t, http.StatusAccepted, code, "%s must not post into the owner's session", name)
	}
	select {
	case m := <-msgs:
		t.Fatalf("foreign POST produced a frame on the owner's stream: %s", m)
	case <-time.After(200 * time.Millisecond):
	}

	require.Equal(t, http.StatusAccepted, postFrame(t, ts.URL, endpoint, owner, initFrame))
	select {
	case m := <-msgs:
		var resp map[string]any
		require.NoError(t, json.Unmarshal([]byte(m), &resp))
		assert.EqualValues(t, 1, resp["id"])
	case <-time.After(2 * time.Second):
		t.Fatal("owner's own POST did not reach the stream")
	}
}

// R-6: the stream closing while a POST is mid-dispatch must not panic the
// POST with "send on closed channel".
func TestSSE_CloseDuringSendNoPanic(t *testing.T) {
	srv := mcp.New(mcp.Config{Logger: zerolog.Nop()})
	h := New(srv, zerolog.Nop())
	owner := newIdent()
	ctx := auth.WithUser(context.Background(), auth.UserInfo{ID: owner.user, TenantID: owner.tenant, Role: "api_key"})
	sess := h.sessions.create(ctx)

	// A tool whose execution closes the session — the GET leg ending while
	// the POST leg is between lookup and enqueue.
	srv.Register(mcp.ToolDef{Name: "closer", Handler: func(context.Context, json.RawMessage) (*mcp.ToolCallResult, error) {
		h.sessions.close(sess.id)
		return &mcp.ToolCallResult{Content: []mcp.ContentBlock{{Type: "text", Text: "ok"}}}, nil
	}})

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"closer","arguments":{}}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mcp/sse?session_id="+sess.id, strings.NewReader(body)).WithContext(ctx)
	rec := httptest.NewRecorder()
	require.NotPanics(t, func() { h.sseInbound(rec, req) })
}
