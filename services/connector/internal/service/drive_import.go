// Google Drive → SeDoc import (ADR 0089). Lists a Drive folder, downloads
// each file (exporting Google-native editor docs to PDF), and runs them
// through the server-side ingest flow so each lands as a real document
// with a version — which fires dms.version.uploaded.v1 and pulls the file
// into OCR + embedding + search, exactly like a browser upload.
package service

import (
	"context"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/aieera/sedoc/pkg/esign"
	"github.com/aieera/sedoc/services/connector/internal/model"
	"github.com/aieera/sedoc/services/connector/internal/providers/google"
)

// maxDriveImportFiles bounds a single import call. A folder with more
// files imports in pages on repeat calls (future: pagination cursor);
// today we cap + report what was left so the caller isn't misled into
// thinking everything came across.
const maxDriveImportFiles = 200

// DriveImportResult summarises one import run.
type DriveImportResult struct {
	Imported    int      `json:"imported"`
	Skipped     int      `json:"skipped"` // folders + unexportable Google-native types
	Failed      int      `json:"failed"`
	Truncated   bool     `json:"truncated"` // hit maxDriveImportFiles
	DocumentIDs []string `json:"document_ids"`
	Errors      []string `json:"errors,omitempty"`
}

// ErrIngestUnavailable — import was attempted but the ingest path isn't
// wired (storage unreachable at boot). Handler maps to 503.
var ErrIngestUnavailable = errors.New("drive import: ingest path not configured")

// ImportDriveFolder imports the files directly under driveFolderID into
// the given SeDoc workspace + folder. actorID is the admin running the
// import (perms + created_by). Partial success is the norm: a single
// file's download/ingest failure is recorded in the result, not fatal.
func (s *Service) ImportDriveFolder(
	ctx context.Context,
	tenantID, actorID, authToken, driveFolderID, workspaceID, destFolderID string,
) (*DriveImportResult, error) {
	if s.ingest == nil {
		return nil, ErrIngestUnavailable
	}
	if workspaceID == "" || destFolderID == "" {
		return nil, fmt.Errorf("workspace_id and folder_id are required")
	}
	if driveFolderID == "" {
		// Drive's well-known alias for "My Drive" root.
		driveFolderID = "root"
	}

	conn, tokens, err := s.googleClient(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	files, err := conn.ListDriveFiles(ctx, tokens, driveFolderID)
	if err != nil {
		return nil, fmt.Errorf("list drive folder: %w", err)
	}

	res := &DriveImportResult{DocumentIDs: []string{}}
	for _, f := range files {
		if res.Imported+res.Failed >= maxDriveImportFiles {
			res.Truncated = true
			break
		}
		fileID, _ := f["id"].(string)
		name, _ := f["name"].(string)
		mimeType, _ := f["mimeType"].(string)
		if fileID == "" {
			continue
		}
		// Nested folders aren't recursed (flat import); skip them.
		if mimeType == "application/vnd.google-apps.folder" {
			res.Skipped++
			continue
		}

		data, filename, contentType, derr := conn.DownloadDriveFile(ctx, tokens, fileID, name, mimeType)
		if errors.Is(derr, google.ErrUnsupportedDriveType) {
			res.Skipped++
			continue
		}
		if derr != nil {
			res.Failed++
			res.Errors = append(res.Errors, fmt.Sprintf("%s: download: %v", name, derr))
			continue
		}

		meta := map[string]any{
			"source":            "google_drive",
			"drive.file_id":     fileID,
			"drive.mime_type":   mimeType,
			"drive.folder_id":   driveFolderID,
			"drive.modified_at": f["modifiedTime"],
		}
		docID, ierr := s.ingest.IngestFile(ctx, tenantID, actorID, authToken, workspaceID, destFolderID, filename, contentType, data, meta)
		if ierr != nil {
			res.Failed++
			res.Errors = append(res.Errors, fmt.Sprintf("%s: ingest: %v", name, ierr))
			continue
		}
		res.Imported++
		res.DocumentIDs = append(res.DocumentIDs, docID)
	}
	s.log.Info().
		Str("tenant_id", tenantID).
		Str("drive_folder", driveFolderID).
		Int("imported", res.Imported).
		Int("skipped", res.Skipped).
		Int("failed", res.Failed).
		Msg("drive import complete")

	// Emit dms.connector.synced.v1 (outbox → NATS, §4.7) so the platform
	// learns a connector sync ran. Best-effort: the import already
	// succeeded, so a failed emit is logged, not fatal.
	if tid, perr := uuid.Parse(tenantID); perr == nil {
		runID, _ := uuid.NewV7()
		payload, _ := stdjson.Marshal(map[string]any{
			"tenant_id":     tenantID,
			"provider":      "google",
			"source":        "drive",
			"source_folder": driveFolderID,
			"workspace_id":  workspaceID,
			"folder_id":     destFolderID,
			"imported":      res.Imported,
			"skipped":       res.Skipped,
			"failed":        res.Failed,
			"truncated":     res.Truncated,
			"synced_at":     time.Now().UTC().Format(time.RFC3339),
		})
		if err := s.repo.EmitOutbox(ctx, tid, "dms.connector.synced.v1", "connector", runID, payload); err != nil {
			s.log.Warn().Err(err).Str("tenant_id", tenantID).Msg("connector.synced emit failed (sync itself succeeded)")
		}
	}
	return res, nil
}

// googleClient resolves the tenant's saved Google credentials + tokens
// and returns a ready connector. Mirrors m365Client. Returns
// ErrNoTenantTokens when the tenant hasn't authorized Drive yet.
func (s *Service) googleClient(ctx context.Context, tenantID string) (*google.Connector, *model.OAuthTokens, error) {
	row, err := s.repo.GetConnector(ctx, tenantID, "google")
	if err != nil {
		return nil, nil, err
	}
	if row == nil {
		return nil, nil, errors.New("google not configured for this tenant; save credentials first")
	}
	if len(row.OAuthTokensEncrypted) == 0 {
		return nil, nil, ErrNoTenantTokens
	}
	cfg, err := s.unsealProviderConfig(row)
	if err != nil {
		return nil, nil, err
	}
	tokPlain, err := esign.UnsealString(string(row.OAuthTokensEncrypted), s.connSealingKey)
	if err != nil {
		return nil, nil, fmt.Errorf("unseal google tokens: %w", err)
	}
	var tokens model.OAuthTokens
	if err := stdjson.Unmarshal([]byte(tokPlain), &tokens); err != nil {
		return nil, nil, fmt.Errorf("decode google tokens: %w", err)
	}
	return google.New(cfg.ClientID, cfg.ClientSecret, s.log), &tokens, nil
}
