package main

import (
	"net/http"
	"time"

	"github.com/aieera/sedoc/pkg/middleware"
)

// DEC-19 — request-body limits and server timeouts for the document HTTP
// surface.
//
// Every route defaults to MaxBody(MaxJSONBody) (1 MiB): the limit sits in
// front of the whole mux tree, so a route added later is limited without
// anyone remembering to wrap it. Routes whose request body IS a document
// (or carries one, base64 / NDJSON) get MaxDocumentBody (64 MiB) and a
// longer read deadline plus a matching write window. Mounts that apply
// their own limit are passed through untouched. Routes that stream a whole
// document or a long-running result (downloads, exports, bulk import)
// clear the server's write deadline for their response.

// documentBodyRoutes carry document bytes in the request body.
var documentBodyRoutes = []string{
	"POST /wopi/files/{file_id}/contents", // WOPI PutFile
	"POST /wopi/files/{file_id}",          // WOPI PutRelativeFile (X-WOPI-Override)
	"/api/v1/integrations/m365/",          // Outlook ingest: base64 body + attachments
	"POST /api/v1/admin/bulk/import",      // NDJSON bulk import
}

// selfLimitedRoutes apply their own body limit at their mount (zero-trust
// shares, OnlyOffice, watermark, IRM, db-info); the default is not stacked
// on top of theirs.
var selfLimitedRoutes = []string{
	"/api/v1/zt/",
	"/api/v1/admin/share-links/zt",
	"/api/v1/admin/share-links/zt/",
	"/api/v1/documents/{document_id}/versions/{version_id}/onlyoffice/",
	"/api/v1/documents/{document_id}/versions/{version_id}/wm/",
	"/api/v1/admin/watermark/",
	"/api/v1/irm/",
	"/api/v1/admin/irm/",
	"/api/v1/admin/platform/db-info",
}

// streamingRoutes write a whole document (or export) in one response; the
// server WriteTimeout would cut a large or slow download, so they clear
// the write deadline for their response.
var streamingRoutes = []string{
	"GET /wopi/files/{file_id}/contents",
	"GET /api/v1/documents/{document_id}/versions/{version_id}/decrypt-stream",
	"GET /api/v1/documents/{document_id}/content",
	"GET /api/v1/documents/{document_id}/versions/{version_id}/unredacted",
	"GET /api/v1/shared/{token}/download",
	"GET /api/v1/admin/bulk/export",
	"POST /api/v1/admin/bulk/import", // NDJSON results streamed with Flush
	"POST /api/v1/admin/ediscovery/export",
	"GET /api/v1/admin/ediscovery/jobs/{id}/download",
	"GET /api/v1/zt/{token_id}/stream",
	"GET /api/v1/irm/licenses/content",
	"GET /api/v1/documents/{document_id}/versions/{version_id}/wm/download",
	"GET /api/v1/documents/{document_id}/versions/{version_id}/wm/pages/{page_number}",
}

// serverTimeouts are the DEC-19 server timeouts. DocumentRead replaces
// Read on the document-body routes, whose 64 MiB bodies may take longer
// than Read to arrive; those routes also get DocumentRead+Write to write
// their response.
type serverTimeouts struct {
	Read, Write, Idle, DocumentRead time.Duration
}

var defaultTimeouts = serverTimeouts{
	Read:         60 * time.Second,
	Write:        120 * time.Second,
	Idle:         120 * time.Second,
	DocumentRead: 10 * time.Minute,
}

// withBodyLimits routes each request through its DEC-19 body limit before
// next (the service's top-level mux) sees it.
func withBodyLimits(next http.Handler) http.Handler {
	return withBodyLimitsConfig(next, defaultTimeouts)
}

func withBodyLimitsConfig(next http.Handler, cfg serverTimeouts) http.Handler {
	jsonLimited := middleware.MaxBody(middleware.MaxJSONBody)(next)
	documentLimited := middleware.MaxBody(middleware.MaxDocumentBody)(next)
	streaming := map[string]bool{}
	for _, p := range streamingRoutes {
		streaming[p] = true
	}
	m := http.NewServeMux()
	m.Handle("/", jsonLimited)
	for _, p := range documentBodyRoutes {
		if streaming[p] {
			// Streams its response for as long as the work runs (bulk
			// import NDJSON results): no write deadline at all.
			m.Handle(p, documentDeadlines(cfg, false, documentLimited))
			delete(streaming, p)
			continue
		}
		m.Handle(p, documentDeadlines(cfg, true, documentLimited))
	}
	for _, p := range selfLimitedRoutes {
		m.Handle(p, next)
	}
	for _, p := range streamingRoutes {
		if streaming[p] {
			m.Handle(p, clearWriteDeadline(jsonLimited))
		}
	}
	return m
}

// clearWriteDeadline lifts the server WriteTimeout for one streaming
// response.
func clearWriteDeadline(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
		next.ServeHTTP(w, r)
	})
}

// documentDeadlines gives a document-carrying request DocumentRead for its
// body and — since the server WriteTimeout runs from the moment the request
// was read, i.e. through the body read and the processing — either
// DocumentRead+Write to write its response (bounded) or no write deadline
// (a streaming response).
func documentDeadlines(cfg serverTimeouts, bounded bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		_ = rc.SetReadDeadline(time.Now().Add(cfg.DocumentRead))
		if bounded {
			_ = rc.SetWriteDeadline(time.Now().Add(cfg.DocumentRead + cfg.Write))
		} else {
			_ = rc.SetWriteDeadline(time.Time{})
		}
		next.ServeHTTP(w, r)
	})
}

// applyTimeouts sets the DEC-19 server timeouts on srv.
func applyTimeouts(srv *http.Server, cfg serverTimeouts) {
	srv.ReadHeaderTimeout = 5 * time.Second
	srv.ReadTimeout = cfg.Read
	srv.WriteTimeout = cfg.Write
	srv.IdleTimeout = cfg.Idle
}

// newHTTPServer builds the document HTTP server with the DEC-19 timeouts.
// h is expected to include withBodyLimits.
func newHTTPServer(addr string, h http.Handler) *http.Server {
	srv := &http.Server{Addr: addr, Handler: h} // #nosec G112 -- applyTimeouts sets ReadTimeout, which net/http also applies to headers while ReadHeaderTimeout is zero
	applyTimeouts(srv, defaultTimeouts)
	return srv
}
