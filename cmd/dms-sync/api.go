package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// errConflict is returned by CreateVersion when the base version is stale (the
// server's 409) — the signal the sync loop turns into a conflicted copy.
var errConflict = errors.New("version conflict (base is stale)")

// errOffline wraps any transport error so the loop can distinguish "server
// unreachable" (retry next tick — offline queue) from a real API rejection.
var errOffline = errors.New("offline")

// Client is a minimal SeDoc REST client authenticated with an API key.
type Client struct {
	base string
	key  string
	hc   *http.Client
}

func newClient(base, key string) *Client {
	return &Client{base: base, key: key, hc: &http.Client{Timeout: 60 * time.Second}}
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+c.key)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errOffline, err)
	}
	return resp, nil
}

// ---- delta ---------------------------------------------------------------

type deltaChange struct {
	Kind      string `json:"kind"`
	Op        string `json:"op"`
	ID        string `json:"id"`
	ParentID  string `json:"parent_id"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	VersionID string `json:"version_id"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
	Mime      string `json:"mime"`
}

type deltaResp struct {
	Changes    []deltaChange `json:"changes"`
	NextCursor string        `json:"next_cursor"`
	HasMore    bool          `json:"has_more"`
}

func (c *Client) Delta(ctx context.Context, workspaceID, deviceID, cursor string) (*deltaResp, error) {
	q := url.Values{}
	if workspaceID != "" {
		q.Set("workspace_id", workspaceID)
	}
	if deviceID != "" {
		q.Set("device_id", deviceID)
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	q.Set("limit", "500")
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/v1/sync/delta?"+q.Encode(), nil)
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, apiErr("delta", resp)
	}
	var out deltaResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DownloadContent returns the current-version plaintext of a document.
func (c *Client) DownloadContent(ctx context.Context, docID string) ([]byte, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/v1/documents/"+docID+"/content", nil)
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, apiErr("download", resp)
	}
	return io.ReadAll(resp.Body)
}

// ---- upload (initiate -> PUT -> complete) --------------------------------

type initiateResp struct {
	UploadID        string `json:"upload_id"`
	PresignedPutURL string `json:"presigned_put_url"`
	Deduplicated    bool   `json:"deduplicated"`
	ExistingBlobID  string `json:"existing_blob_id"`
}
type completeResp struct {
	ContentBlobID string `json:"content_blob_id"`
}

// UploadBlob pushes bytes and returns the content_blob_id, using sha256 dedup so
// unchanged content is a no-op PUT.
func (c *Client) UploadBlob(ctx context.Context, data []byte, filename, mime, sha, workspaceID, folderID string) (string, error) {
	initBody, _ := json.Marshal(map[string]any{
		"filename": filename, "mime_type": mime, "size_bytes": len(data),
		"checksum_sha256": sha, "workspace_id": workspaceID, "folder_id": folderID,
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/v1/storage/uploads/initiate", bytes.NewReader(initBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.do(req)
	if err != nil {
		return "", err
	}
	var ir initiateResp
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return "", apiErr("initiate", resp)
	}
	_ = json.NewDecoder(resp.Body).Decode(&ir)
	_ = resp.Body.Close()
	if ir.Deduplicated && ir.ExistingBlobID != "" {
		return ir.ExistingBlobID, nil
	}
	// PUT bytes to the presigned URL (direct to object storage).
	putReq, _ := http.NewRequestWithContext(ctx, http.MethodPut, ir.PresignedPutURL, bytes.NewReader(data))
	putReq.Header.Set("Content-Type", mime)
	putResp, err := c.hc.Do(putReq)
	if err != nil {
		return "", fmt.Errorf("%w: put: %v", errOffline, err)
	}
	_ = putResp.Body.Close()
	if putResp.StatusCode/100 != 2 {
		return "", fmt.Errorf("presigned put: status %d", putResp.StatusCode)
	}
	compBody, _ := json.Marshal(map[string]any{"checksum_sha256": sha, "size_bytes": len(data)})
	compReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/v1/storage/uploads/"+ir.UploadID+"/complete", bytes.NewReader(compBody))
	compReq.Header.Set("Content-Type", "application/json")
	compResp, err := c.do(compReq)
	if err != nil {
		return "", err
	}
	defer func() { _ = compResp.Body.Close() }()
	if compResp.StatusCode != http.StatusOK {
		return "", apiErr("complete", compResp)
	}
	var cr completeResp
	if err := json.NewDecoder(compResp.Body).Decode(&cr); err != nil {
		return "", err
	}
	return cr.ContentBlobID, nil
}

type versionResp struct {
	ID string `json:"id"`
}

// CreateVersion links a blob to a document as a new version, passing
// base_version_id for optimistic concurrency. A 409 becomes errConflict.
func (c *Client) CreateVersion(ctx context.Context, docID, blobID, baseVersionID, summary string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"content_blob_id": blobID, "change_summary": summary, "base_version_id": baseVersionID,
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/v1/documents/"+docID+"/versions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusConflict {
		return "", errConflict
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", apiErr("create_version", resp)
	}
	var vr versionResp
	if err := json.NewDecoder(resp.Body).Decode(&vr); err != nil {
		return "", err
	}
	return vr.ID, nil
}

func apiErr(op string, resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("%s: status %s: %s", op, strconv.Itoa(resp.StatusCode), string(b))
}
