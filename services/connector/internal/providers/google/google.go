// Package google implements the Google Workspace connector: OAuth, Gmail
// ingestion, Drive migration.
package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/services/connector/internal/model"
	"github.com/aieera/sedoc/services/connector/internal/providers"
)

const gmailBaseURL = "https://gmail.googleapis.com/gmail/v1"
const driveBaseURL = "https://www.googleapis.com/drive/v3"

// Connector implements Google Workspace integration.
type Connector struct {
	providers.BaseOAuth
	log zerolog.Logger
}

// New creates a Google Workspace connector.
func New(clientID, clientSecret string, log zerolog.Logger) *Connector {
	return &Connector{
		BaseOAuth: providers.BaseOAuth{
			ProviderName:  "google_workspace",
			AuthEndpoint:  "https://accounts.google.com/o/oauth2/v2/auth",
			TokenEndpoint: "https://oauth2.googleapis.com/token",
			Scopes:        []string{"https://www.googleapis.com/auth/gmail.readonly", "https://www.googleapis.com/auth/drive.readonly"},
			ClientID:      clientID,
			ClientSecret:  clientSecret,
			Log:           log,
		},
		log: log,
	}
}

func (c *Connector) Name() string { return "google_workspace" }

// PollGmail fetches recent unread messages.
func (c *Connector) PollGmail(ctx context.Context, tokens *model.OAuthTokens) ([]map[string]any, error) {
	tokens, err := c.EnsureValid(ctx, tokens)
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		gmailBaseURL+"/users/me/messages?q=is:unread&maxResults=20", nil)
	req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 429 {
		return nil, fmt.Errorf("gmail rate limited")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("gmail %d: %s", resp.StatusCode, string(body[:min(len(body), 300)]))
	}
	var result struct {
		Messages []map[string]any `json:"messages"`
	}
	_ = json.Unmarshal(body, &result)
	return result.Messages, nil
}

// ListDriveFiles lists files from Google Drive for migration.
func (c *Connector) ListDriveFiles(ctx context.Context, tokens *model.OAuthTokens, folderID string) ([]map[string]any, error) {
	tokens, err := c.EnsureValid(ctx, tokens)
	if err != nil {
		return nil, err
	}
	q := url.QueryEscape(fmt.Sprintf("'%s' in parents and trashed = false", folderID))
	// supportsAllDrives so shared drives + items shared with the user are
	// reachable, not just My Drive. pageSize 100 is one page; the importer
	// pages with nextPageToken via ListDriveFilesPage below.
	reqURL := driveBaseURL + "/files?q=" + q +
		"&fields=files(id,name,mimeType,size,modifiedTime),nextPageToken" +
		"&pageSize=100&supportsAllDrives=true&includeItemsFromAllDrives=true"
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("drive list %d: %s", resp.StatusCode, string(body))
	}
	var result struct {
		Files []map[string]any `json:"files"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&result)
	return result.Files, nil
}

// googleExportFormats maps a Google-native MIME type to the format we
// export it to for storage in SeDoc. We standardise on PDF — it's
// universally previewable by the document viewer, OCR-able by the
// intelligence pipeline, and lossless enough for an archival DMS. The
// value pairs the export MIME with the filename extension we append.
var googleExportFormats = map[string]struct{ mime, ext string }{
	"application/vnd.google-apps.document":     {"application/pdf", ".pdf"},
	"application/vnd.google-apps.spreadsheet":  {"application/pdf", ".pdf"},
	"application/vnd.google-apps.presentation": {"application/pdf", ".pdf"},
	"application/vnd.google-apps.drawing":      {"application/pdf", ".pdf"},
}

// ErrUnsupportedDriveType is returned by DownloadDriveFile for Google-
// native types we can't export (Forms, Sites, Shortcuts, Maps, …) and
// for folder pseudo-files. The importer skips these rather than failing
// the whole batch.
var ErrUnsupportedDriveType = errors.New("google: drive file type not importable")

// DownloadDriveFile fetches one Drive file's bytes. Binary uploads
// (PDF, Office, images) stream via ?alt=media; Google-native editor
// files (Docs/Sheets/Slides/Drawings) are exported to PDF since the raw
// bytes aren't downloadable. Returns the bytes plus the resolved
// filename + content-type to store under. Google-native types we can't
// export return ErrUnsupportedDriveType so the caller can skip them.
func (c *Connector) DownloadDriveFile(ctx context.Context, tokens *model.OAuthTokens, fileID, name, mimeType string) (data []byte, filename, contentType string, err error) {
	tokens, err = c.EnsureValid(ctx, tokens)
	if err != nil {
		return nil, "", "", err
	}

	var reqURL string
	filename = name
	contentType = mimeType

	if strings.HasPrefix(mimeType, "application/vnd.google-apps.") {
		fmtInfo, ok := googleExportFormats[mimeType]
		if !ok {
			// Folders + Forms/Sites/etc. — nothing to materialise.
			return nil, "", "", ErrUnsupportedDriveType
		}
		reqURL = fmt.Sprintf("%s/files/%s/export?mimeType=%s&supportsAllDrives=true",
			driveBaseURL, fileID, url.QueryEscape(fmtInfo.mime))
		contentType = fmtInfo.mime
		if !strings.HasSuffix(strings.ToLower(filename), fmtInfo.ext) {
			filename += fmtInfo.ext
		}
	} else {
		reqURL = fmt.Sprintf("%s/files/%s?alt=media&supportsAllDrives=true", driveBaseURL, fileID)
	}

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, "", "", fmt.Errorf("drive download %s (%d): %s", fileID, resp.StatusCode, string(body))
	}
	// Cap a single Drive file at 100 MiB to bound memory — the importer
	// holds the whole file in RAM to compute the sha256 + presigned PUT.
	data, err = io.ReadAll(io.LimitReader(resp.Body, 100<<20))
	if err != nil {
		return nil, "", "", fmt.Errorf("read drive body: %w", err)
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return data, filename, contentType, nil
}
