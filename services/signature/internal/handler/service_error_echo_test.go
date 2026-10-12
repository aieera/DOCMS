package handler

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/services/signature/internal/service"
)

// TestWriteServiceError_UpstreamDetailNotEchoed: ErrUpstream wraps the
// e-sign / QES vendor's error text (`%w: %v`); the 502 body must carry only
// the fixed sentinel message, never the vendor detail.
func TestWriteServiceError_UpstreamDetailNotEchoed(t *testing.T) {
	h := &Handler{log: zerolog.Nop()}
	rec := httptest.NewRecorder()
	vendor := errors.New(`docusign: 500 {"errorCode":"INTERNAL","token":"eyJsecret"}`)
	h.writeServiceError(rec, fmt.Errorf("%w: qes register: %v", service.ErrUpstream, vendor), "x")

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "docusign") || strings.Contains(body, "eyJsecret") || strings.Contains(body, "qes register") {
		t.Fatalf("502 body echoes upstream detail: %s", body)
	}
	if !strings.Contains(body, service.ErrUpstream.Error()) {
		t.Fatalf("502 body = %s; want the generic %q", body, service.ErrUpstream.Error())
	}
}

// TestWriteServiceError_4xxSentinelsKeepTheirMessage: the 4xx sentinels wrap
// only fixed text or the caller's own request state, so their message stays.
func TestWriteServiceError_4xxSentinelsKeepTheirMessage(t *testing.T) {
	h := &Handler{log: zerolog.Nop()}
	rec := httptest.NewRecorder()
	h.writeServiceError(rec, fmt.Errorf("%w: request is cancelled", service.ErrRequestNotSignable), "x")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "request is cancelled") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}
