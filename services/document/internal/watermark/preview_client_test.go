package watermark

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// H-17 / DEC-4: the preview REST API is internal-only — every route
// requires X-Internal-Service-Key == SEDOC_INTERNAL_API_KEY (or the preview
// X-Service-Key) and answers 401 otherwise. Status() sent no key at all, so
// the viewer's page count broke. Every call to the preview service now
// carries the internal key, the preview service key and the gateway
// signature.
func TestPreviewClient_SendsInternalKey(t *testing.T) {
	var seen []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Clone())
		if r.Header.Get("X-Internal-Service-Key") != "internal-k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/api/v1/previews/doc-1/status" {
			_, _ = w.Write([]byte(`{"status":"ready","page_count":3}`))
			return
		}
		_, _ = w.Write([]byte("png"))
	}))
	defer srv.Close()

	c := NewPreviewClient(srv.URL, "svc-k", "internal-k", "gw-secret")
	ctx := context.Background()

	st, err := c.Status(ctx, "tenant-1", "doc-1", "ver-1")
	require.NoError(t, err)
	require.Equal(t, "ready", st.Status)
	require.Equal(t, 3, *st.PageCount)

	_, code, err := c.StampPage(ctx, PageStampRequest{TenantID: "tenant-1"})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)
	_, code, err = c.StampPDF(ctx, []byte("%PDF"), "wm", 30, 45, true, 24, "#000")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)

	require.Len(t, seen, 3)
	for _, h := range seen {
		require.Equal(t, "internal-k", h.Get("X-Internal-Service-Key"))
		require.Equal(t, "svc-k", h.Get("X-Service-Key"))
		require.Equal(t, "gw-secret", h.Get("X-Gateway-Signature"))
	}
}
