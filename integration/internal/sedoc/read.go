package sedoc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// Read-side operations the Files BFF proxies to SeDoc. The worker only writes;
// these power the file-explorer / review / search surfaces.

// SetSearchURL points the Search method at the SeDoc search service (a separate
// service from the document gateway). Empty → Search returns an error.
func (c *Client) SetSearchURL(u string) { c.searchURL = trimSlash(u) }

// Folder is a folder row as returned by the document gateway.
type Folder struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	ParentFolderID   string `json:"parentFolderId"`
	DocumentCount    int64  `json:"documentCount"`
	ChildFolderCount int64  `json:"childFolderCount"`
}

// ListFoldersResult is one keyset page of children.
type ListFoldersResult struct {
	Folders       []Folder `json:"folders"`
	NextPageToken string   `json:"nextPageToken"`
}

// ListFolders returns the children of a parent folder (or workspace root when
// parentID is empty), keyset-paginated (Workstream 6).
func (c *Client) ListFolders(ctx context.Context, workspaceID, parentID, pageToken string, pageSize int) (*ListFoldersResult, error) {
	q := url.Values{}
	if parentID != "" {
		q.Set("parent_folder_id", parentID)
	}
	if pageToken != "" {
		q.Set("page_token", pageToken)
	}
	if pageSize > 0 {
		q.Set("page_size", fmt.Sprint(pageSize))
	}
	var out ListFoldersResult
	if err := c.doJSON(ctx, http.MethodGet,
		"/workspaces/"+workspaceID+"/folders?"+q.Encode(), "", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetFolder fetches a single folder.
func (c *Client) GetFolder(ctx context.Context, folderID string) (*Folder, error) {
	var out Folder
	if err := c.doJSON(ctx, http.MethodGet, "/folders/"+folderID, "", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Document is a document row (subset the BFF surfaces).
type Document struct {
	ID               string `json:"id"`
	Title            string `json:"title"`
	FolderID         string `json:"folderId"`
	DocumentClass    string `json:"documentClass"`
	LifecycleState   string `json:"lifecycleState"`
	CurrentVersionID string `json:"currentVersionId"`
	MimeType         string `json:"mimeType"`
	TotalSizeBytes   int64  `json:"totalSizeBytes"`
	ExternalID       string `json:"externalId"`
	UpdatedAt        string `json:"updatedAt"`
}

// ListDocumentsResult is one page of documents.
type ListDocumentsResult struct {
	Documents  []Document `json:"documents"`
	Pagination struct {
		NextPageToken string `json:"nextPageToken"`
	} `json:"pagination"`
}

// ListDocuments lists documents in a folder, keyset-paginated. The document
// gateway already permission-filters rows (Workstream 7), but the BFF's API key
// is a single service principal, so scope is enforced by the BFF (folder ∈
// authorized customer subtree) rather than per-user OPA here.
func (c *Client) ListDocuments(ctx context.Context, workspaceID, folderID, pageToken string, pageSize int) (*ListDocumentsResult, error) {
	q := url.Values{}
	q.Set("folder_id", folderID)
	if pageToken != "" {
		q.Set("pagination.page_token", pageToken)
	}
	if pageSize > 0 {
		q.Set("pagination.page_size", fmt.Sprint(pageSize))
	}
	var out ListDocumentsResult
	if err := c.doJSON(ctx, http.MethodGet,
		"/workspaces/"+workspaceID+"/documents?"+q.Encode(), "", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetDocument fetches a document (used to resolve folder → customer for authz).
func (c *Client) GetDocument(ctx context.Context, id string) (*Document, error) {
	var out Document
	if err := c.doJSON(ctx, http.MethodGet, "/documents/"+id, "", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Version is one version row.
type Version struct {
	ID            string `json:"id"`
	VersionNumber int    `json:"versionNumber"`
	SizeBytes     int64  `json:"sizeBytes"`
	MimeType      string `json:"mimeType"`
	CreatedByName string `json:"createdByName"`
	CreatedAt     string `json:"createdAt"`
	Label         string `json:"label"`
}

// ListVersionsResult is the versions list.
type ListVersionsResult struct {
	Versions []Version `json:"versions"`
}

// ListVersions returns a document's version history.
func (c *Client) ListVersions(ctx context.Context, documentID string) (*ListVersionsResult, error) {
	var out ListVersionsResult
	if err := c.doJSON(ctx, http.MethodGet, "/documents/"+documentID+"/versions", "", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// StreamContent opens the current version's bytes for streaming. The caller MUST
// close the returned reader. The BFF io.Copy's this straight to the browser so
// large downloads stay flat-memory and the storage key never reaches the client.
func (c *Client) StreamContent(ctx context.Context, documentID string) (io.ReadCloser, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/documents/"+documentID+"/content", nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		return nil, "", parseAPIError(resp, raw)
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/octet-stream"
	}
	return resp.Body, ct, nil
}

// RestoreVersion restores a prior version as the new head.
func (c *Client) RestoreVersion(ctx context.Context, idemKey, documentID, versionID string) error {
	return c.doJSON(ctx, http.MethodPost,
		"/documents/"+documentID+"/versions/"+versionID+"/restore", idemKey, map[string]any{}, nil)
}

// ---- Review queue ----------------------------------------------------------

// RawJSON proxies an opaque JSON body through (the BFF forwards SeDoc's shape).
type RawJSON = json.RawMessage

// ReviewQueueList returns the review queue (admin), passing the cursor through.
func (c *Client) ReviewQueueList(ctx context.Context, status, cursor string, limit int) (RawJSON, error) {
	q := url.Values{}
	if status != "" {
		q.Set("status", status)
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if limit > 0 {
		q.Set("limit", fmt.Sprint(limit))
	}
	var out json.RawMessage
	if err := c.doJSON(ctx, http.MethodGet, "/review-queue?"+q.Encode(), "", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ReviewQueueGet returns one review item + its OCR text.
func (c *Client) ReviewQueueGet(ctx context.Context, id string) (RawJSON, error) {
	var out json.RawMessage
	if err := c.doJSON(ctx, http.MethodGet, "/review-queue/"+id, "", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ReviewQueueResolve applies a reviewer decision.
func (c *Client) ReviewQueueResolve(ctx context.Context, idemKey, id string, body RawJSON) (RawJSON, error) {
	var out json.RawMessage
	if err := c.doJSON(ctx, http.MethodPost, "/review-queue/"+id+"/resolve", idemKey, body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ---- Search (separate service) ---------------------------------------------

// Search runs a query against the SeDoc search service. The BFF injects the
// customer's folder filter into body before calling so results stay scoped.
func (c *Client) Search(ctx context.Context, body any) (RawJSON, error) {
	if c.searchURL == "" {
		return nil, fmt.Errorf("search url not configured")
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.searchURL+"/search", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseAPIError(resp, raw)
	}
	return json.RawMessage(raw), nil
}
