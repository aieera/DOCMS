package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Actor is the authenticated caller on whose behalf the service acts.
type Actor struct {
	TenantID string
	UserID   string
	Role     string
	Email    string
}

// maxSigningPDF bounds the bytes read for a QES hash or an e-sign send. A
// larger version is refused rather than silently truncated.
const maxSigningPDF = 64 << 20

// AddDocumentFetcher wires the reader that QES start and e-sign send use to
// obtain the bytes of a request's own (document, version) — DEC-17: the
// server never signs or sends client-supplied bytes. docHTTPBase is the
// document service's internal HTTP base (e.g. http://document:8080).
func (s *Service) AddDocumentFetcher(docHTTPBase, internalKey, gatewaySecret string) {
	f := &documentFetcher{
		base: docHTTPBase, internalKey: internalKey, gatewaySecret: gatewaySecret,
		client: &http.Client{Timeout: 90 * time.Second},
	}
	s.versionBytesFn = f.fetch
}

// versionBytes returns the decrypted bytes of (documentID, versionID) read
// as actor.
func (s *Service) versionBytes(ctx context.Context, actor Actor, documentID, versionID string) ([]byte, error) {
	if s.versionBytesFn == nil {
		return nil, errors.New("document fetch not configured")
	}
	return s.versionBytesFn(ctx, actor, documentID, versionID)
}

type documentFetcher struct {
	base          string
	internalKey   string
	gatewaySecret string
	client        *http.Client
}

// fetch GETs the document service's decrypt-stream endpoint through its
// internal-service path, naming the caller (X-User-ID / X-User-Role) as the
// acting identity so the document service's own view check applies to the
// caller — the signature service never reads a document the caller could
// not read themselves.
func (d *documentFetcher) fetch(ctx context.Context, actor Actor, documentID, versionID string) ([]byte, error) {
	if actor.TenantID == "" || actor.UserID == "" || actor.Role == "" {
		// The internal-service path requires a known X-User-Role (an
		// absent role is 401); a fetch on a user's behalf must name the
		// user's own role, never a fabricated one.
		return nil, errors.New("document fetch: tenant, user and role required")
	}
	url := fmt.Sprintf("%s/api/v1/documents/%s/versions/%s/decrypt-stream", d.base, documentID, versionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set(internalServiceKeyHeader, d.internalKey)
	req.Header.Set("X-Auth-Tenant-ID", actor.TenantID)
	req.Header.Set("X-User-ID", actor.UserID)
	req.Header.Set("X-User-Role", actor.Role)
	if d.gatewaySecret != "" {
		req.Header.Set("X-Gateway-Signature", d.gatewaySecret)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("document fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized:
		return nil, fmt.Errorf("%w: caller cannot read the request's document version", ErrForbidden)
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%w: document version not found", ErrRequestNotFound)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("document fetch %s: status %d", versionID, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSigningPDF+1))
	if err != nil {
		return nil, fmt.Errorf("document fetch: %w", err)
	}
	if len(body) > maxSigningPDF {
		return nil, fmt.Errorf("document version exceeds %d bytes", maxSigningPDF)
	}
	if len(body) == 0 {
		return nil, errors.New("document fetch returned no bytes")
	}
	return body, nil
}
