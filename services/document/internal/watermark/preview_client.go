// Package watermark holds the document service's HTTP client to the preview
// service's internal stamping endpoints (§5). The document service owns viewer
// identity + the tenant/classification config and resolves the final watermark
// text; the preview service owns the rendering libs + cached base pages and
// does the actual drawing.
package watermark

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"
)

// PreviewClient calls the preview service's internal, service-authed stamping
// endpoints. Zero value (empty baseURL/serviceKey) is "disabled": Enabled()
// reports false and the caller falls back to unwatermarked bytes.
//
// H-17 / DEC-4: the whole preview REST API is internal-only — its
// require_internal_caller dependency accepts X-Internal-Service-Key ==
// SEDOC_INTERNAL_API_KEY or X-Service-Key == SEDOC_SERVICE_API_KEY and
// answers 401 otherwise. Every request carries both keys and the gateway
// signature (authorize), so no call depends on which one a route checks.
type PreviewClient struct {
	baseURL       string
	serviceKey    string // SEDOC_SERVICE_API_KEY → X-Service-Key
	internalKey   string // SEDOC_INTERNAL_API_KEY → X-Internal-Service-Key
	gatewaySecret string // SEDOC_GATEWAY_SECRET → X-Gateway-Signature
	hc            *http.Client
}

func NewPreviewClient(baseURL, serviceKey, internalKey, gatewaySecret string) *PreviewClient {
	return &PreviewClient{
		baseURL:       baseURL,
		serviceKey:    serviceKey,
		internalKey:   internalKey,
		gatewaySecret: gatewaySecret,
		hc:            &http.Client{Timeout: 30 * time.Second},
	}
}

// authorize stamps the internal-caller credentials on a request to the
// preview service. Empty values are not sent.
func (c *PreviewClient) authorize(req *http.Request) {
	if c.internalKey != "" {
		req.Header.Set("X-Internal-Service-Key", c.internalKey)
	}
	if c.serviceKey != "" {
		req.Header.Set("X-Service-Key", c.serviceKey)
	}
	if c.gatewaySecret != "" {
		req.Header.Set("X-Gateway-Signature", c.gatewaySecret)
	}
}

// Enabled reports whether the client is configured to reach the preview service.
func (c *PreviewClient) Enabled() bool {
	return c != nil && c.baseURL != "" && c.serviceKey != ""
}

// PageStampRequest is the JSON body for POST /previews/internal/watermark/page.
type PageStampRequest struct {
	TenantID    string `json:"tenant_id"`
	DocumentID  string `json:"document_id"`
	VersionID   string `json:"version_id"`
	PageNumber  int    `json:"page_number"`
	Thumbnail   bool   `json:"thumbnail"`
	Text        string `json:"text"`
	Opacity     int    `json:"opacity"`
	RotationDeg int    `json:"rotation_deg"`
	Tile        bool   `json:"tile"`
	FontSize    int    `json:"font_size"`
	Color       string `json:"color"`
}

// StampPage returns the watermarked PNG plus the preview HTTP status (so the
// caller can propagate 404 "not ready" vs other failures).
func (c *PreviewClient) StampPage(ctx context.Context, req PageStampRequest) ([]byte, int, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, 0, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/v1/previews/internal/watermark/page", bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	c.authorize(httpReq)
	return c.do(httpReq)
}

// StampPDF burns the watermark into a PDF (download/print). It streams the PDF
// bytes as multipart form-data alongside the style fields.
func (c *PreviewClient) StampPDF(ctx context.Context, pdf []byte, text string, opacity, rotationDeg int, tile bool, fontSize int, color string) ([]byte, int, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "document.pdf")
	if err != nil {
		return nil, 0, err
	}
	if _, err := fw.Write(pdf); err != nil {
		return nil, 0, err
	}
	fields := map[string]string{
		"text":         text,
		"opacity":      fmt.Sprintf("%d", opacity),
		"rotation_deg": fmt.Sprintf("%d", rotationDeg),
		"tile":         fmt.Sprintf("%t", tile),
		"font_size":    fmt.Sprintf("%d", fontSize),
		"color":        color,
	}
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			return nil, 0, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, 0, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/v1/previews/internal/watermark/pdf", &buf)
	if err != nil {
		return nil, 0, err
	}
	httpReq.Header.Set("Content-Type", mw.FormDataContentType())
	c.authorize(httpReq)
	return c.do(httpReq)
}

// PreviewStatus mirrors the preview /status response subset the viewer needs.
type PreviewStatus struct {
	Status    string `json:"status"`
	PageCount *int   `json:"page_count"`
}

// Status queries the preview service's (internal-only) status endpoint for
// page count.
func (c *PreviewClient) Status(ctx context.Context, tenantID, documentID, versionID string) (PreviewStatus, error) {
	url := fmt.Sprintf("%s/api/v1/previews/%s/status?tenant_id=%s&version_id=%s",
		c.baseURL, documentID, tenantID, versionID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return PreviewStatus{}, err
	}
	c.authorize(httpReq)
	resp, err := c.hc.Do(httpReq)
	if err != nil {
		return PreviewStatus{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return PreviewStatus{Status: "none"}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return PreviewStatus{}, fmt.Errorf("preview status: unexpected %d", resp.StatusCode)
	}
	var out PreviewStatus
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return PreviewStatus{}, err
	}
	return out, nil
}

func (c *PreviewClient) do(req *http.Request) ([]byte, int, error) {
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("preview stamp: status %d: %s", resp.StatusCode, truncate(data, 200))
	}
	return data, resp.StatusCode, nil
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n])
	}
	return string(b)
}
