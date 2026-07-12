// Package google implements the Google Workspace connector: OAuth, Gmail
// ingestion, Drive migration.
package google

import (
	"context"
	"crypto/md5" //nolint:gosec // Drive integrity metadata, not a security boundary
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/services/connector/internal/model"
	"github.com/aieera/sedoc/services/connector/internal/providers"
)

const gmailBaseURL = "https://gmail.googleapis.com/gmail/v1"

// driveBaseURL is a var so download tests can stand in a fake Drive
// server; production code never reassigns it.
var driveBaseURL = "https://www.googleapis.com/drive/v3"

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
		"&fields=files(id,name,mimeType,size,md5Checksum,sha256Checksum,modifiedTime),nextPageToken" +
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

// SIZE POLICY (decided, documented): imports are capped at
// maxDriveFileBytes and an oversized file FAILS ITS ITEM with an
// explicit error — never truncate-and-succeed. The previous shape
// (io.LimitReader with no overflow check) silently cut files >100 MiB
// at the limit and stored the corrupt prefix as a "successful" import.
//
// Why a hard cap instead of streaming: the ingest flow (and the storage
// service itself — CompleteUpload buffers the full object for
// hash/scan/encrypt; multipart upload is the B1.1 follow-up) is single-
// PUT and memory-bound end to end. Streaming in the connector would
// only move the same buffer one hop. When storage grows a multipart
// surface, raise/remove this cap alongside it.
//
// A var, not a const, so tests can exercise the overflow path without a
// 100 MiB fixture.
var maxDriveFileBytes int64 = 100 << 20

// ErrFileTooLarge — the file exceeds maxDriveFileBytes. The importer
// records it as a per-item failure (visible in the result + the
// connector.synced sync history), never a silent partial.
var ErrFileTooLarge = errors.New("google: drive file exceeds the import size limit")

// DriveFileMeta is the integrity metadata Drive declares for a binary
// (non-Google-native) file. Zero values mean "not declared" — Google-
// native exports have none (the exported bytes differ from the stored
// ones).
type DriveFileMeta struct {
	SizeBytes      int64
	MD5Checksum    string
	SHA256Checksum string
}

// DriveMetaFromFile extracts DriveFileMeta from a files.list entry.
// Drive v3 serialises `size` as a STRING of the int64.
func DriveMetaFromFile(f map[string]any) DriveFileMeta {
	var m DriveFileMeta
	if s, ok := f["size"].(string); ok {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			m.SizeBytes = n
		}
	}
	m.MD5Checksum, _ = f["md5Checksum"].(string)
	m.SHA256Checksum, _ = f["sha256Checksum"].(string)
	return m
}

// DownloadDriveFile fetches one Drive file's bytes. Binary uploads
// (PDF, Office, images) stream via ?alt=media; Google-native editor
// files (Docs/Sheets/Slides/Drawings) are exported to PDF since the raw
// bytes aren't downloadable. Returns the bytes plus the resolved
// filename + content-type to store under. Google-native types we can't
// export return ErrUnsupportedDriveType so the caller can skip them.
//
// Size + integrity (see the SIZE POLICY above maxDriveFileBytes):
//   - a binary file whose DECLARED size exceeds the cap fails fast with
//     ErrFileTooLarge before a single byte is fetched;
//   - the read detects overflow (cap+1 sniff) and fails with
//     ErrFileTooLarge — never a silent truncation;
//   - binary downloads are verified against Drive's declared metadata:
//     exact size, and sha256Checksum (preferred) or md5Checksum. A
//     mismatch fails the item. Exports carry no declared metadata (the
//     exported bytes differ from the stored ones) so only the overflow
//     check applies.
func (c *Connector) DownloadDriveFile(ctx context.Context, tokens *model.OAuthTokens, fileID, name, mimeType string, meta DriveFileMeta) (data []byte, filename, contentType string, err error) {
	tokens, err = c.EnsureValid(ctx, tokens)
	if err != nil {
		return nil, "", "", err
	}

	var reqURL string
	filename = name
	contentType = mimeType
	isExport := false

	if strings.HasPrefix(mimeType, "application/vnd.google-apps.") {
		fmtInfo, ok := googleExportFormats[mimeType]
		if !ok {
			// Folders + Forms/Sites/etc. — nothing to materialise.
			return nil, "", "", ErrUnsupportedDriveType
		}
		isExport = true
		reqURL = fmt.Sprintf("%s/files/%s/export?mimeType=%s&supportsAllDrives=true",
			driveBaseURL, fileID, url.QueryEscape(fmtInfo.mime))
		contentType = fmtInfo.mime
		if !strings.HasSuffix(strings.ToLower(filename), fmtInfo.ext) {
			filename += fmtInfo.ext
		}
	} else {
		if meta.SizeBytes > maxDriveFileBytes {
			return nil, "", "", fmt.Errorf("%w: %q is %d bytes, limit is %d",
				ErrFileTooLarge, name, meta.SizeBytes, maxDriveFileBytes)
		}
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
	// cap+1 sniff: reading one byte past the limit distinguishes
	// "exactly at the cap" from "over it" without unbounded buffering.
	data, err = io.ReadAll(io.LimitReader(resp.Body, maxDriveFileBytes+1))
	if err != nil {
		return nil, "", "", fmt.Errorf("read drive body: %w", err)
	}
	if int64(len(data)) > maxDriveFileBytes {
		return nil, "", "", fmt.Errorf("%w: %q download exceeded the %d byte limit",
			ErrFileTooLarge, name, maxDriveFileBytes)
	}

	// Integrity: what we read must be exactly what Drive declared.
	// (Binary files only — exports have no stored-bytes metadata.)
	if !isExport {
		if meta.SizeBytes > 0 && int64(len(data)) != meta.SizeBytes {
			return nil, "", "", fmt.Errorf("drive download %q: got %d bytes, drive declared %d (incomplete read)",
				name, len(data), meta.SizeBytes)
		}
		if meta.SHA256Checksum != "" {
			sum := sha256.Sum256(data)
			if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, meta.SHA256Checksum) {
				return nil, "", "", fmt.Errorf("drive download %q: sha256 mismatch (got %s, drive declared %s)",
					name, got, meta.SHA256Checksum)
			}
		} else if meta.MD5Checksum != "" {
			sum := md5.Sum(data) //nolint:gosec // integrity check against Drive's declared md5, not a security boundary
			if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, meta.MD5Checksum) {
				return nil, "", "", fmt.Errorf("drive download %q: md5 mismatch (got %s, drive declared %s)",
					name, got, meta.MD5Checksum)
			}
		}
	}

	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return data, filename, contentType, nil
}
