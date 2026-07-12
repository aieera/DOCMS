// Regression: Drive imports must never truncate-and-succeed.
//
// The old DownloadDriveFile wrapped the body in io.LimitReader(_, 100MiB)
// with no overflow check — a larger file read back exactly 100 MiB of
// prefix, was reported as imported, and the corrupt bytes were stored as
// a "successful" document. These tests pin the fixed contract:
//
//   - declared size over the cap    → ErrFileTooLarge, no media fetch;
//   - streamed body over the cap    → ErrFileTooLarge (cap+1 sniff);
//   - bytes shorter than declared   → explicit integrity error;
//   - checksum mismatch             → explicit integrity error;
//   - matching size + sha256        → success with exact bytes;
//   - Google-native export          → success, no declared-meta checks.
//
// The cap is lowered per-test (maxDriveFileBytes is a package var) so
// the overflow path runs without a 100 MiB fixture.
package google

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/services/connector/internal/model"
)

// fakeDrive serves /files/{id}?alt=media with the given payload and
// counts media hits so fail-fast tests can assert no fetch happened.
type fakeDrive struct {
	payload   []byte
	mediaHits int
}

func (f *fakeDrive) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.RawQuery, "alt=media"):
			f.mediaHits++
			_, _ = w.Write(f.payload)
		case strings.Contains(r.URL.Path, "/export"):
			_, _ = w.Write(f.payload)
		default:
			http.NotFound(w, r)
		}
	})
}

func testConnector(t *testing.T, payload []byte) (*Connector, *model.OAuthTokens, *fakeDrive) {
	t.Helper()
	fd := &fakeDrive{payload: payload}
	srv := httptest.NewServer(fd.handler())
	t.Cleanup(srv.Close)

	orig := driveBaseURL
	driveBaseURL = srv.URL
	t.Cleanup(func() { driveBaseURL = orig })

	tokens := &model.OAuthTokens{
		AccessToken: "test-token",
		TokenExpiry: time.Now().Add(time.Hour), // EnsureValid short-circuits
	}
	return New("id", "secret", zerolog.Nop()), tokens, fd
}

func lowerCap(t *testing.T, n int64) {
	t.Helper()
	orig := maxDriveFileBytes
	maxDriveFileBytes = n
	t.Cleanup(func() { maxDriveFileBytes = orig })
}

func shaHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestDownload_DeclaredOversize_FailsFastWithoutFetching(t *testing.T) {
	c, tokens, fd := testConnector(t, []byte("irrelevant"))
	lowerCap(t, 1024)

	_, _, _, err := c.DownloadDriveFile(context.Background(), tokens,
		"f1", "big.bin", "application/octet-stream",
		DriveFileMeta{SizeBytes: 150 << 20}) // declares 150 MiB
	if !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("want ErrFileTooLarge, got %v", err)
	}
	if fd.mediaHits != 0 {
		t.Fatalf("oversized file must fail BEFORE downloading; media hits = %d", fd.mediaHits)
	}
}

func TestDownload_StreamedOverflow_FailsNotTruncates(t *testing.T) {
	// Server streams more than the cap; declared size lies (or is absent).
	c, tokens, _ := testConnector(t, bytes.Repeat([]byte("x"), 4096))
	lowerCap(t, 1024)

	data, _, _, err := c.DownloadDriveFile(context.Background(), tokens,
		"f2", "sneaky.bin", "application/octet-stream", DriveFileMeta{})
	if !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("want ErrFileTooLarge on overflow, got err=%v data=%d bytes", err, len(data))
	}
	if data != nil {
		t.Fatal("no truncated bytes may be returned on overflow")
	}
}

func TestDownload_ShortRead_FailsIntegrity(t *testing.T) {
	payload := []byte("only half the promised bytes")
	c, tokens, _ := testConnector(t, payload)

	_, _, _, err := c.DownloadDriveFile(context.Background(), tokens,
		"f3", "cut.bin", "application/pdf",
		DriveFileMeta{SizeBytes: int64(len(payload)) * 2})
	if err == nil || !strings.Contains(err.Error(), "incomplete read") {
		t.Fatalf("want incomplete-read integrity error, got %v", err)
	}
}

func TestDownload_ChecksumMismatch_Fails(t *testing.T) {
	payload := []byte("tampered or corrupted in flight")
	c, tokens, _ := testConnector(t, payload)

	_, _, _, err := c.DownloadDriveFile(context.Background(), tokens,
		"f4", "doc.pdf", "application/pdf",
		DriveFileMeta{SizeBytes: int64(len(payload)), SHA256Checksum: shaHex([]byte("different bytes"))})
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("want sha256 mismatch error, got %v", err)
	}
}

func TestDownload_VerifiedHappyPath(t *testing.T) {
	payload := []byte("the full, correct file contents")
	c, tokens, _ := testConnector(t, payload)

	data, filename, contentType, err := c.DownloadDriveFile(context.Background(), tokens,
		"f5", "report.pdf", "application/pdf",
		DriveFileMeta{SizeBytes: int64(len(payload)), SHA256Checksum: shaHex(payload)})
	if err != nil {
		t.Fatalf("verified download failed: %v", err)
	}
	if !bytes.Equal(data, payload) {
		t.Fatal("bytes must round-trip exactly")
	}
	if filename != "report.pdf" || contentType != "application/pdf" {
		t.Fatalf("filename/contentType: %q %q", filename, contentType)
	}
}

func TestDownload_ExportPath_NoDeclaredMetaChecks(t *testing.T) {
	// Google-native export: Drive's metadata describes the NATIVE file,
	// not the exported PDF — size/checksum checks must not apply.
	payload := []byte("%PDF-1.7 exported document")
	c, tokens, _ := testConnector(t, payload)

	data, filename, contentType, err := c.DownloadDriveFile(context.Background(), tokens,
		"f6", "My Doc", "application/vnd.google-apps.document",
		DriveFileMeta{SizeBytes: 999999, SHA256Checksum: shaHex([]byte("native-bytes"))})
	if err != nil {
		t.Fatalf("export download failed: %v", err)
	}
	if !bytes.Equal(data, payload) {
		t.Fatal("export bytes must round-trip")
	}
	if filename != "My Doc.pdf" || contentType != "application/pdf" {
		t.Fatalf("export naming: %q %q", filename, contentType)
	}
}

func TestDownload_ExportOverflow_StillFails(t *testing.T) {
	c, tokens, _ := testConnector(t, bytes.Repeat([]byte("y"), 4096))
	lowerCap(t, 1024)

	_, _, _, err := c.DownloadDriveFile(context.Background(), tokens,
		"f7", "Huge Sheet", "application/vnd.google-apps.spreadsheet", DriveFileMeta{})
	if !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("export overflow must also fail, got %v", err)
	}
}

func TestDriveMetaFromFile_ParsesDriveShapes(t *testing.T) {
	m := DriveMetaFromFile(map[string]any{
		"size":           "1048576", // Drive v3 serialises int64 as string
		"md5Checksum":    "aabb",
		"sha256Checksum": "ccdd",
	})
	if m.SizeBytes != 1048576 || m.MD5Checksum != "aabb" || m.SHA256Checksum != "ccdd" {
		t.Fatalf("parsed %+v", m)
	}
	empty := DriveMetaFromFile(map[string]any{"name": "no meta"})
	if empty.SizeBytes != 0 || empty.MD5Checksum != "" || empty.SHA256Checksum != "" {
		t.Fatalf("zero meta expected, got %+v", empty)
	}
}
