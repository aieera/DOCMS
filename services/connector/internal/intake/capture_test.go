package intake

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// fakeIngester records IngestFile calls and can be scripted to fail specific
// segments, so we test the orchestration (titles, metadata, best-effort skip)
// without storage/document services.
type fakeIngester struct {
	calls []ingestCall
	fail  map[int]bool // segment index → return an error
	n     int
}

type ingestCall struct {
	filename string
	data     []byte
	meta     map[string]any
}

func (f *fakeIngester) IngestFile(_ context.Context, _, _, _, _, _ string, filename, _ string, data []byte, meta map[string]any) (string, error) {
	i := len(f.calls)
	f.calls = append(f.calls, ingestCall{filename: filename, data: data, meta: meta})
	if f.fail[i] {
		return "", errors.New("ingest boom")
	}
	f.n++
	return fmt.Sprintf("doc-%d", f.n), nil
}

func splitServer(t *testing.T, segmentBodies []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/capture/split" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		docs := make([]map[string]any, len(segmentBodies))
		for i, b := range segmentBodies {
			docs[i] = map[string]any{"pdf_b64": base64.StdEncoding.EncodeToString([]byte(b)), "page_count": 1}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"documents": docs})
	}))
}

func newTestCapture(intelURL string, ing ingester) *CaptureService {
	return &CaptureService{intelBase: intelURL, ingest: ing, httpc: &http.Client{Timeout: 5 * time.Second}, log: zerolog.Nop()}
}

func baseInput() CaptureCommitInput {
	return CaptureCommitInput{
		TenantID: "t", ActorID: "u", AuthToken: "tok",
		WorkspaceID: "ws", FolderID: "fo",
		BundleB64: base64.StdEncoding.EncodeToString([]byte("bundle")), Mime: "application/pdf",
		PageGroups: [][]int{{0, 1}, {2}, {3}},
	}
}

func TestCommit_IngestsEachSegmentWithTitleAndMetadata(t *testing.T) {
	ts := splitServer(t, []string{"PDF-A", "PDF-B", "PDF-C"})
	defer ts.Close()
	fi := &fakeIngester{fail: map[int]bool{}}
	svc := newTestCapture(ts.URL, fi)

	in := baseInput()
	in.Segments = []CaptureSegmentMeta{
		{Title: "Invoice 1", Metadata: map[string]any{"invoice_no": "INV-1"}},
		{Title: "", Metadata: nil}, // default title
		{Title: "Contract", Metadata: map[string]any{"ref": "C-9"}},
	}

	ids, err := svc.Commit(context.Background(), in)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if len(ids) != 3 || len(fi.calls) != 3 {
		t.Fatalf("want 3 ids + 3 ingest calls, got ids=%d calls=%d", len(ids), len(fi.calls))
	}
	// Title → filename; default when blank.
	if fi.calls[0].filename != "Invoice 1.pdf" {
		t.Fatalf("filename[0] = %q", fi.calls[0].filename)
	}
	if fi.calls[1].filename != "Scan segment 2.pdf" {
		t.Fatalf("default filename[1] = %q", fi.calls[1].filename)
	}
	// The decoded segment bytes are ingested verbatim.
	if string(fi.calls[2].data) != "PDF-C" {
		t.Fatalf("data[2] = %q", fi.calls[2].data)
	}
	// Provenance metadata is always stamped; mapped barcode metadata carried through.
	if fi.calls[0].meta["capture.source"] != "scan_separation" {
		t.Fatal("missing capture.source")
	}
	if fi.calls[0].meta["capture.segment_index"] != 0 {
		t.Fatalf("capture.segment_index[0] = %v", fi.calls[0].meta["capture.segment_index"])
	}
	if fi.calls[0].meta["invoice_no"] != "INV-1" {
		t.Fatal("mapped metadata not carried through")
	}
}

func TestCommit_BestEffortSkipsFailedSegment(t *testing.T) {
	ts := splitServer(t, []string{"A", "B", "C"})
	defer ts.Close()
	fi := &fakeIngester{fail: map[int]bool{1: true}} // middle segment fails
	svc := newTestCapture(ts.URL, fi)

	ids, err := svc.Commit(context.Background(), baseInput())
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if len(fi.calls) != 3 {
		t.Fatalf("all 3 segments attempted; got %d", len(fi.calls))
	}
	if len(ids) != 2 {
		t.Fatalf("one failure should be skipped → 2 ids, got %d", len(ids))
	}
}

func TestCommit_NoIngestConfigured(t *testing.T) {
	svc := newTestCapture("http://unused", nil)
	if _, err := svc.Commit(context.Background(), baseInput()); err == nil {
		t.Fatal("expected error when ingest pipeline not configured")
	}
}

func TestCommit_IntelligenceErrorSurfaces(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer ts.Close()
	svc := newTestCapture(ts.URL, &fakeIngester{fail: map[int]bool{}})
	if _, err := svc.Commit(context.Background(), baseInput()); err == nil {
		t.Fatal("expected error when intelligence split fails")
	}
}
