package erp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// HTTPClient talks to the (real or mock) ERP's render + authz endpoints.
type HTTPClient struct {
	baseURL string
	hc      *http.Client
}

// NewHTTPClient constructs an ERP client. baseURL is the ERP root (no trailing /).
func NewHTTPClient(baseURL string) *HTTPClient {
	for len(baseURL) > 0 && baseURL[len(baseURL)-1] == '/' {
		baseURL = baseURL[:len(baseURL)-1]
	}
	return &HTTPClient{baseURL: baseURL, hc: &http.Client{Timeout: 30 * time.Second}}
}

// Fetch implements DocumentSource — GET /erp/documents/{fileRef}/pdf.
func (c *HTTPClient) Fetch(ctx context.Context, fileRef string) ([]byte, string, error) {
	u := c.baseURL + "/erp/documents/" + url.PathEscape(fileRef) + "/pdf"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("erp render %s: %d", fileRef, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return nil, "", err
	}
	mime := resp.Header.Get("Content-Type")
	if mime == "" {
		mime = "application/pdf"
	}
	return data, mime, nil
}

// CanAccessCustomer implements Authorizer — GET /erp/authz/customer/{ref}?user=.
// Used by the BFF before proxying any per-customer request to SeDoc.
func (c *HTTPClient) CanAccessCustomer(ctx context.Context, userID, customerRef string) (bool, error) {
	u := c.baseURL + "/erp/authz/customer/" + url.PathEscape(customerRef) + "?user=" + url.QueryEscape(userID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	// 200 = allowed, 403 = denied, anything else = error (fail closed).
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusForbidden:
		return false, nil
	default:
		return false, fmt.Errorf("erp authz: %d", resp.StatusCode)
	}
}
