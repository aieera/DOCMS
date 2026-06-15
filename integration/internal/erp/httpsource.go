package erp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// HTTPClient talks to the (real or mock) ERP's render + authz + listing
// endpoints.
type HTTPClient struct {
	baseURL string
	token   string // optional bearer the integration presents to the ERP
	hc      *http.Client
}

// NewHTTPClient constructs an ERP client. baseURL is the ERP root (no trailing /).
func NewHTTPClient(baseURL string) *HTTPClient {
	for len(baseURL) > 0 && baseURL[len(baseURL)-1] == '/' {
		baseURL = baseURL[:len(baseURL)-1]
	}
	return &HTTPClient{baseURL: baseURL, hc: &http.Client{Timeout: 30 * time.Second}}
}

// WithToken sets a bearer token presented to the ERP's render/authz/listing
// endpoints (real ERPs typically require auth; the mock needs none). Empty token
// = no Authorization header. Returns the client for chaining, so the mock→real
// cutover is config-only (set ERP_API_TOKEN) with no code change.
func (c *HTTPClient) WithToken(token string) *HTTPClient {
	c.token = token
	return c
}

// auth stamps the bearer token on an outbound ERP request when configured.
func (c *HTTPClient) auth(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

// Fetch implements DocumentSource — GET /erp/documents/{fileRef}/pdf.
func (c *HTTPClient) Fetch(ctx context.Context, fileRef string) ([]byte, string, error) {
	u := c.baseURL + "/erp/documents/" + url.PathEscape(fileRef) + "/pdf"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", err
	}
	c.auth(req)
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

// ListCustomers implements Lister — GET /erp/customers (backfill inventory).
func (c *HTTPClient) ListCustomers(ctx context.Context) ([]Customer, error) {
	var out struct {
		Customers []Customer `json:"customers"`
	}
	if err := c.getJSON(ctx, "/erp/customers", &out); err != nil {
		return nil, err
	}
	return out.Customers, nil
}

// ListDocuments implements Lister — GET /erp/customers/{ref}/documents.
func (c *HTTPClient) ListDocuments(ctx context.Context, customerRef string) ([]DocumentRef, error) {
	var out struct {
		Documents []DocumentRef `json:"documents"`
	}
	if err := c.getJSON(ctx, "/erp/customers/"+url.PathEscape(customerRef)+"/documents", &out); err != nil {
		return nil, err
	}
	return out.Documents, nil
}

// getJSON GETs an ERP path and decodes a 2xx JSON body into out.
func (c *HTTPClient) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	c.auth(req)
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("erp GET %s: %d", path, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(out)
}

// CanAccessCustomer implements Authorizer — GET /erp/authz/customer/{ref}?user=.
// Used by the BFF before proxying any per-customer request to SeDoc.
func (c *HTTPClient) CanAccessCustomer(ctx context.Context, userID, customerRef string) (bool, error) {
	u := c.baseURL + "/erp/authz/customer/" + url.PathEscape(customerRef) + "?user=" + url.QueryEscape(userID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false, err
	}
	c.auth(req)
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
