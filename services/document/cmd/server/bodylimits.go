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
// longer read deadline. Mounts that apply their own limit are passed
// through untouched. Download routes that stream a whole document clear
// the server's write deadline for their response.

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
	"POST /api/v1/admin/ediscovery/export",
	"GET /api/v1/admin/ediscovery/jobs/{id}/download",
	"GET /api/v1/zt/{token_id}/stream",
	"GET /api/v1/irm/licenses/content",
	"GET /api/v1/documents/{document_id}/versions/{version_id}/wm/download",
	"GET /api/v1/documents/{document_id}/versions/{version_id}/wm/pages/{page_number}",
}

// Server timeouts (DEC-19). documentReadTimeout replaces ReadTimeout on the
// document-body routes, whose 64 MiB bodies may take longer than 60 s.
const (
	serverReadTimeout   = 60 * time.Second
	serverWriteTimeout  = 120 * time.Second
	serverIdleTimeout   = 120 * time.Second
	documentReadTimeout = 10 * time.Minute
)

// withBodyLimits routes each request through its DEC-19 body limit before
// next (the service's top-level mux) sees it.
func withBodyLimits(next http.Handler) http.Handler {
	jsonLimited := middleware.MaxBody(middleware.MaxJSONBody)(next)
	documentLimited := extendReadDeadline(middleware.MaxBody(middleware.MaxDocumentBody)(next))
	m := http.NewServeMux()
	m.Handle("/", jsonLimited)
	for _, p := range documentBodyRoutes {
		m.Handle(p, documentLimited)
	}
	for _, p := range selfLimitedRoutes {
		m.Handle(p, next)
	}
	for _, p := range streamingRoutes {
		m.Handle(p, clearWriteDeadline(jsonLimited))
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

// extendReadDeadline gives a document-carrying request body more than the
// server ReadTimeout to arrive.
func extendReadDeadline(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(documentReadTimeout))
		next.ServeHTTP(w, r)
	})
}

// newHTTPServer builds the document HTTP server with the DEC-19 timeouts.
// h is expected to include withBodyLimits.
func newHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       serverReadTimeout,
		WriteTimeout:      serverWriteTimeout,
		IdleTimeout:       serverIdleTimeout,
	}
}
