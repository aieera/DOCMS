package m365

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/vaultdms/vaultdms/services/connector/internal/model"
)

// graphBaseURL is the Microsoft Graph v1.0 root. We deliberately
// don't expose this as a config knob — `beta` endpoints aren't
// covered by Microsoft's stability SLA and the prompt's feature set
// is all in v1.0.
const graphBaseURL = "https://graph.microsoft.com/v1.0"

// Client wraps the per-tenant token + the parent Connector that
// knows how to refresh it. Constructed via Connector.NewClient(tokens).
//
// All methods take an optional `actingAs` user GUID to support
// delegated calls when the connector holds an app-only token. When
// `actingAs` is empty the call falls back to `/me` semantics
// (delegated user token). Mixing app + delegated tokens in the same
// row is intentionally NOT supported — the admin chooses one mode at
// connect time.
type Client struct {
	conn       *Connector
	tokens     *model.OAuthTokens
	httpc      *http.Client
	tokensMu   sync.Mutex // serialises refresh; we never want two
	                     // concurrent refresh calls because Entra
	                     // can issue different refresh_tokens for
	                     // each, leaving one stale.
	onRefresh  func(*model.OAuthTokens) // optional callback the
	                                    // service layer uses to
	                                    // re-seal the new tokens
	                                    // into connector_configs.
}

// NewClient builds a Client around the given tokens. The Connector
// MUST be alive for the lifetime of the Client because token refresh
// goes back through it.
func (c *Connector) NewClient(tokens *model.OAuthTokens, opts ...ClientOption) *Client {
	cl := &Client{
		conn:   c,
		tokens: tokens,
		httpc:  &http.Client{Timeout: 30 * time.Second},
	}
	for _, opt := range opts {
		opt(cl)
	}
	return cl
}

// ClientOption is the functional-options shape callers use for the
// refresh-callback + custom http.Client wiring.
type ClientOption func(*Client)

// WithRefreshCallback installs a callback fired whenever the client
// refreshes the access token. The service layer uses this to re-seal
// the new tokens into connector_configs.oauth_tokens_encrypted so the
// next process to load them gets the current refresh_token.
func WithRefreshCallback(cb func(*model.OAuthTokens)) ClientOption {
	return func(c *Client) { c.onRefresh = cb }
}

// WithHTTPClient swaps the inner http.Client — primarily for tests
// that need a roundtripper stub.
func WithHTTPClient(h *http.Client) ClientOption {
	return func(c *Client) { c.httpc = h }
}

// =====================================================================
// Public surface
// =====================================================================

// Site is a SharePoint site as returned by /sites.
type Site struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Name        string `json:"name"`
	WebURL      string `json:"webUrl"`
}

// DriveItem covers both folders and files. The shape mirrors Graph's
// driveItem resource; we keep only the fields the connector needs.
type DriveItem struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	WebURL      string    `json:"webUrl"`
	Size        int64     `json:"size"`
	MimeType    string    `json:"mimeType,omitempty"`
	IsFolder    bool      `json:"is_folder"`
	IsFile      bool      `json:"is_file"`
	LastMod     time.Time `json:"lastModifiedDateTime"`
	ParentPath  string    `json:"parent_path,omitempty"`
}

// Message is one Outlook mail item (minimal projection).
type Message struct {
	ID        string    `json:"id"`
	Subject   string    `json:"subject"`
	FromAddr  string    `json:"from"`
	Received  time.Time `json:"receivedDateTime"`
	BodyType  string    `json:"body_type"` // "html" | "text"
	BodyText  string    `json:"body"`
	IsRead    bool      `json:"isRead"`
}

// SendMailRequest is the input to SendMail. Only the minimum surface
// — full Graph parity is out of scope for this prompt.
type SendMailRequest struct {
	From    string   // optional; defaults to the connected user's mailbox
	To      []string // required, ≥1 address
	CC      []string
	Subject string
	BodyHTML string
}

// ListSites returns every SharePoint site the connected user (or
// app, for app-only tokens) can read. Falls through to the
// /sites?search=* shape because plain /sites is restricted in
// app-only auth.
func (c *Client) ListSites(ctx context.Context, actingAs string) ([]Site, error) {
	u := "/sites?search=*"
	if actingAs != "" {
		u = "/users/" + url.PathEscape(actingAs) + "/followedSites"
	}
	var resp struct {
		Value []Site `json:"value"`
	}
	if err := c.graphGet(ctx, u, &resp); err != nil {
		return nil, err
	}
	return resp.Value, nil
}

// ListDriveItems lists items in a drive folder. `folderID` may be
// empty — that resolves to the drive's root.
func (c *Client) ListDriveItems(ctx context.Context, driveID, folderID, actingAs string) ([]DriveItem, error) {
	var u string
	switch {
	case folderID == "":
		u = "/drives/" + url.PathEscape(driveID) + "/root/children"
	default:
		u = "/drives/" + url.PathEscape(driveID) + "/items/" + url.PathEscape(folderID) + "/children"
	}
	if actingAs != "" {
		// Drives are user-scoped or site-scoped — for delegated calls
		// the /users/<id>/drive/... shape mirrors the same surface.
		u = "/users/" + url.PathEscape(actingAs) + u
	}
	// 200 items per page — Graph defaults to 100 which surprises
	// callers who paginate by `@odata.nextLink`. The page-link path
	// would be a Phase-2 follow-up if we hit drives larger than 200
	// items in a single folder.
	u += paramJoin(u) + "$top=200"
	var resp struct {
		Value []rawDriveItem `json:"value"`
	}
	if err := c.graphGet(ctx, u, &resp); err != nil {
		return nil, err
	}
	out := make([]DriveItem, 0, len(resp.Value))
	for _, r := range resp.Value {
		out = append(out, r.materialize())
	}
	return out, nil
}

// GetDriveItemContent streams a file's bytes. The caller MUST Close
// the returned ReadCloser — large PDFs / videos are not buffered.
func (c *Client) GetDriveItemContent(ctx context.Context, driveID, itemID, actingAs string) (io.ReadCloser, error) {
	u := "/drives/" + url.PathEscape(driveID) + "/items/" + url.PathEscape(itemID) + "/content"
	if actingAs != "" {
		u = "/users/" + url.PathEscape(actingAs) + u
	}
	resp, err := c.do(ctx, http.MethodGet, u, nil, "")
	if err != nil {
		return nil, err
	}
	// Graph 302s to the actual content URL; the standard http client
	// follows the redirect for us. If we ended up with a non-2xx
	// nevertheless, surface as error.
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("driveItem content status %d: %s", resp.StatusCode, snippet(body))
	}
	return resp.Body, nil
}

// UploadDriveItem PUTs `content` into a new file beneath `parentID`
// in `driveID`. For files >4 MB Graph requires an upload-session +
// chunked PUT — this method takes the simple path and rejects
// anything larger. Phase 2 wires the chunked path.
const smallFileLimit = 4 * 1024 * 1024 // 4 MB

func (c *Client) UploadDriveItem(ctx context.Context, driveID, parentID, name, actingAs string, content io.Reader) (*DriveItem, error) {
	buf, err := io.ReadAll(io.LimitReader(content, smallFileLimit+1))
	if err != nil {
		return nil, fmt.Errorf("read content: %w", err)
	}
	if len(buf) > smallFileLimit {
		return nil, fmt.Errorf("file >4 MB; chunked upload not yet supported (Phase 2)")
	}
	u := fmt.Sprintf("/drives/%s/items/%s:/%s:/content",
		url.PathEscape(driveID), url.PathEscape(parentID), url.PathEscape(name))
	if actingAs != "" {
		u = "/users/" + url.PathEscape(actingAs) + u
	}
	var item rawDriveItem
	if err := c.graphPut(ctx, u, "application/octet-stream", bytes.NewReader(buf), &item); err != nil {
		return nil, err
	}
	r := item.materialize()
	return &r, nil
}

// ListMessages lists messages in a mailbox folder. `mailbox` is
// either empty (= /me/messages) or a user GUID for an admin-impersonated
// read. `filter` is a raw $filter expression — caller's responsibility
// to URL-escape special characters; Graph's filter grammar is
// extensive enough that wrapping it would be lossy.
func (c *Client) ListMessages(ctx context.Context, mailbox, filter, actingAs string) ([]Message, error) {
	box := "/me/mailFolders/inbox/messages"
	if mailbox != "" {
		box = "/users/" + url.PathEscape(mailbox) + "/mailFolders/inbox/messages"
	}
	if actingAs != "" && mailbox == "" {
		box = "/users/" + url.PathEscape(actingAs) + "/mailFolders/inbox/messages"
	}
	q := url.Values{}
	q.Set("$select", "id,subject,from,receivedDateTime,body,isRead")
	q.Set("$top", "50")
	if filter != "" {
		q.Set("$filter", filter)
	}
	u := box + "?" + q.Encode()
	var resp struct {
		Value []rawMessage `json:"value"`
	}
	if err := c.graphGet(ctx, u, &resp); err != nil {
		return nil, err
	}
	out := make([]Message, 0, len(resp.Value))
	for _, m := range resp.Value {
		out = append(out, m.materialize())
	}
	return out, nil
}

// SendMail posts a mail item via /sendMail. Body is HTML; plain-text
// callers should html-escape themselves. Graph's response is empty
// 202, so success = no error.
func (c *Client) SendMail(ctx context.Context, msg SendMailRequest, actingAs string) error {
	if len(msg.To) == 0 {
		return errors.New("SendMail: To required")
	}
	u := "/me/sendMail"
	if actingAs != "" {
		u = "/users/" + url.PathEscape(actingAs) + "/sendMail"
	}
	body := map[string]any{
		"message": map[string]any{
			"subject": msg.Subject,
			"body":    map[string]any{"contentType": "HTML", "content": msg.BodyHTML},
			"toRecipients": toRecipients(msg.To),
			"ccRecipients": toRecipients(msg.CC),
		},
		"saveToSentItems": "true",
	}
	return c.graphPostNoResp(ctx, u, body)
}

// PostChannelMessage posts a message to a Teams channel. `text` is
// rendered as HTML; pass plain text and Teams escapes it.
func (c *Client) PostChannelMessage(ctx context.Context, teamID, channelID, text, actingAs string) error {
	if teamID == "" || channelID == "" {
		return errors.New("PostChannelMessage: team_id + channel_id required")
	}
	u := fmt.Sprintf("/teams/%s/channels/%s/messages",
		url.PathEscape(teamID), url.PathEscape(channelID))
	body := map[string]any{
		"body": map[string]any{"contentType": "html", "content": text},
	}
	// Channel-post supports app-only auth in some configurations but
	// most tenants gate it behind delegated. actingAs is a no-op for
	// /teams/.../messages — Graph reads the acting principal from
	// the bearer token; we keep the parameter for API symmetry.
	_ = actingAs
	return c.graphPostNoResp(ctx, u, body)
}

// =====================================================================
// HTTP / token-refresh plumbing
// =====================================================================

// do is the low-level HTTP roundtrip with 401-on-invalid-token retry.
// Returns the raw response so streaming callers (GetDriveItemContent)
// can read directly.
func (c *Client) do(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	// Always re-read the body on retry: callers passing an
	// io.Reader can't seek, so we copy into a buffer when a body
	// is present.
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = io.ReadAll(body)
		if err != nil {
			return nil, fmt.Errorf("read request body: %w", err)
		}
	}

	doOnce := func(token string) (*http.Response, error) {
		var rdr io.Reader
		if bodyBytes != nil {
			rdr = bytes.NewReader(bodyBytes)
		}
		req, err := http.NewRequestWithContext(ctx, method, graphBaseURL+path, rdr)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		return c.httpc.Do(req)
	}

	tok, err := c.currentToken(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := doOnce(tok)
	if err != nil {
		return nil, err
	}
	// 401 + Bearer error="invalid_token" is Microsoft's signal that
	// the access token expired mid-flight (clock skew, revoked
	// session, etc.). Refresh once and retry. Subsequent failures
	// surface as-is so the caller can decide whether to back off.
	if resp.StatusCode == http.StatusUnauthorized && isInvalidTokenWWWAuth(resp.Header.Get("WWW-Authenticate")) {
		resp.Body.Close()
		fresh, refreshErr := c.forceRefresh(ctx)
		if refreshErr != nil {
			return nil, fmt.Errorf("401 + refresh failed: %w", refreshErr)
		}
		return doOnce(fresh)
	}
	return resp, nil
}

func (c *Client) currentToken(ctx context.Context) (string, error) {
	c.tokensMu.Lock()
	defer c.tokensMu.Unlock()
	// 60-second pre-expiry budget mirrors BaseOAuth.EnsureValid;
	// having it here too means callers that hold the Client for
	// hours don't pay the 401-retry tax on every call.
	if time.Now().Before(c.tokens.TokenExpiry.Add(-60 * time.Second)) {
		return c.tokens.AccessToken, nil
	}
	return c.refreshLocked(ctx)
}

func (c *Client) forceRefresh(ctx context.Context) (string, error) {
	c.tokensMu.Lock()
	defer c.tokensMu.Unlock()
	return c.refreshLocked(ctx)
}

func (c *Client) refreshLocked(ctx context.Context) (string, error) {
	fresh, err := c.conn.RefreshToken(ctx, c.tokens)
	if err != nil {
		return "", err
	}
	c.tokens = fresh
	if c.onRefresh != nil {
		// Snapshot before unlocking — callback runs without the
		// lock held to avoid deadlocking the seal-into-DB path.
		snap := *fresh
		go c.onRefresh(&snap)
	}
	return fresh.AccessToken, nil
}

// isInvalidTokenWWWAuth parses RFC 6750 WWW-Authenticate. Microsoft
// always returns `error="invalid_token"` for expired/revoked tokens;
// other errors (insufficient_scope, etc.) shouldn't trigger refresh.
func isInvalidTokenWWWAuth(h string) bool {
	if h == "" {
		return false
	}
	low := strings.ToLower(h)
	return strings.Contains(low, `error="invalid_token"`)
}

func (c *Client) graphGet(ctx context.Context, path string, into any) error {
	resp, err := c.do(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("graph GET %s status %d: %s", path, resp.StatusCode, snippet(body))
	}
	return json.Unmarshal(body, into)
}

func (c *Client) graphPut(ctx context.Context, path, contentType string, body io.Reader, into any) error {
	resp, err := c.do(ctx, http.MethodPut, path, body, contentType)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("graph PUT %s status %d: %s", path, resp.StatusCode, snippet(respBody))
	}
	if into != nil && len(respBody) > 0 {
		return json.Unmarshal(respBody, into)
	}
	return nil
}

func (c *Client) graphPostNoResp(ctx context.Context, path string, body any) error {
	bb, _ := json.Marshal(body)
	resp, err := c.do(ctx, http.MethodPost, path, bytes.NewReader(bb), "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("graph POST %s status %d: %s", path, resp.StatusCode, snippet(respBody))
	}
	return nil
}

// snippet trims long error bodies so the log line stays readable.
func snippet(b []byte) string {
	const max = 300
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "…"
}

func paramJoin(u string) string {
	if strings.Contains(u, "?") {
		return "&"
	}
	return "?"
}

func toRecipients(addrs []string) []map[string]any {
	out := make([]map[string]any, 0, len(addrs))
	for _, a := range addrs {
		if a == "" {
			continue
		}
		out = append(out, map[string]any{
			"emailAddress": map[string]any{"address": a},
		})
	}
	return out
}
