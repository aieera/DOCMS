// Scan-capture separation orchestrator (capture pipeline).
//
// The image-heavy work (rasterize pages, decode barcodes/patch-codes, split a
// PDF/TIFF into per-document files) lives in the Python intelligence service —
// it already has PyMuPDF + Pillow + the OCR stack. This orchestrator:
//
//  1. Analyze: proxy the uploaded bundle to intelligence, which returns the
//     proposed split (page thumbnails + segment grouping) for the preview UI.
//  2. Commit: send the user-corrected grouping back to intelligence to split,
//     then ingest each segment via the shared ingest pipeline — so every
//     segment lands as a document + version → one dms.version.uploaded.v1.
package intake

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/services/connector/internal/ingest"
)

// ingester is the slice of *ingest.Client that Commit needs, extracted so the
// orchestration (split → decode → ingest per segment, best-effort) is
// unit-testable without live storage/document services.
type ingester interface {
	IngestFile(ctx context.Context, tenantID, actorID, authToken, workspaceID, folderID, filename, contentType string, data []byte, customMetadata map[string]any) (string, error)
}

// CaptureService orchestrates bundle separation across intelligence + ingest.
type CaptureService struct {
	intelBase string
	ingest    ingester
	httpc     *http.Client
	log       zerolog.Logger
}

// NewCaptureService reads the intelligence base URL from SEDOC_INTELLIGENCE_URL
// (default http://intelligence:8000). ingestClient may be nil — Commit then
// returns a clear "not configured" error, matching the Drive-import path.
func NewCaptureService(ingestClient *ingest.Client, log zerolog.Logger) *CaptureService {
	base := os.Getenv("SEDOC_INTELLIGENCE_URL")
	if base == "" {
		base = "http://intelligence:8000"
	}
	s := &CaptureService{
		intelBase: strings.TrimRight(base, "/"),
		httpc:     &http.Client{Timeout: 120 * time.Second},
		log:       log,
	}
	// Keep the field a nil interface (not a non-nil interface wrapping a nil
	// pointer) when no client is wired, so Commit's nil-guard still fires.
	if ingestClient != nil {
		s.ingest = ingestClient
	}
	return s
}

// Analyze proxies the analyze request body verbatim to intelligence and returns
// the raw split proposal (page_count, page_barcodes, page_thumbnails, segments).
func (s *CaptureService) Analyze(ctx context.Context, reqBody []byte) (json.RawMessage, error) {
	return s.postIntel(ctx, "/internal/v1/capture/analyze", reqBody)
}

// CaptureSegmentMeta carries the per-segment title + barcode→metadata mapping
// the user set in the preview UI.
type CaptureSegmentMeta struct {
	Title    string         `json:"title"`
	Metadata map[string]any `json:"metadata"`
}

// CaptureCommitInput is the corrected grouping plus the ingest target/identity.
type CaptureCommitInput struct {
	TenantID    string
	ActorID     string
	AuthToken   string
	WorkspaceID string
	FolderID    string
	BundleB64   string
	Mime        string
	PageGroups  [][]int
	Segments    []CaptureSegmentMeta
}

type captureSplitResp struct {
	Documents []struct {
		PDFB64    string `json:"pdf_b64"`
		PageCount int    `json:"page_count"`
	} `json:"documents"`
}

// Commit splits the corrected grouping and ingests each segment as a document.
// Returns the created document ids. Best-effort per segment: one failure is
// logged and skipped, not fatal, so a single bad page-range doesn't lose the
// rest of the bundle.
func (s *CaptureService) Commit(ctx context.Context, in CaptureCommitInput) ([]string, error) {
	if s.ingest == nil {
		return nil, fmt.Errorf("capture: ingest pipeline not configured")
	}
	splitReq, _ := json.Marshal(map[string]any{
		"bundle_b64":  in.BundleB64,
		"mime":        in.Mime,
		"page_groups": in.PageGroups,
	})
	raw, err := s.postIntel(ctx, "/internal/v1/capture/split", splitReq)
	if err != nil {
		return nil, fmt.Errorf("capture split: %w", err)
	}
	var sr captureSplitResp
	if err := json.Unmarshal(raw, &sr); err != nil {
		return nil, fmt.Errorf("capture split decode: %w", err)
	}

	docIDs := make([]string, 0, len(sr.Documents))
	for i, d := range sr.Documents {
		pdf, derr := base64.StdEncoding.DecodeString(d.PDFB64)
		if derr != nil {
			s.log.Warn().Int("segment", i).Msg("capture: bad segment pdf base64; skipped")
			continue
		}
		title := fmt.Sprintf("Scan segment %d", i+1)
		meta := map[string]any{}
		if i < len(in.Segments) {
			if in.Segments[i].Title != "" {
				title = in.Segments[i].Title
			}
			for k, v := range in.Segments[i].Metadata {
				meta[k] = v
			}
		}
		meta["capture.source"] = "scan_separation"
		meta["capture.segment_index"] = i
		docID, ierr := s.ingest.IngestFile(ctx, in.TenantID, in.ActorID, in.AuthToken,
			in.WorkspaceID, in.FolderID, title+".pdf", "application/pdf", pdf, meta)
		if ierr != nil {
			s.log.Warn().Err(ierr).Int("segment", i).Msg("capture: segment ingest failed")
			continue
		}
		docIDs = append(docIDs, docID)
	}
	return docIDs, nil
}

func (s *CaptureService) postIntel(ctx context.Context, path string, body []byte) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.intelBase+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("intelligence unreachable: %w", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 128<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("intelligence %s: %d %s", path, resp.StatusCode, string(out))
	}
	return out, nil
}
