package service

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/storage/internal/model"
	"github.com/vaultdms/vaultdms/services/storage/internal/scanner"
)

// Unit-level coverage for the Blueprint §22 upload-pipeline gates. Full
// end-to-end (MinIO + ClamAV + Postgres) lives under tests/integration/
// behind the `integration` build tag; this file pins the pure-Go slices
// so a regression shows up without spinning containers.

// --- 1. Upload size enforcement — 413 with plan-aware message ----------

func TestValidateInitiate_RejectsOverPlanCeiling_With413(t *testing.T) {
	const standardCeiling = int64(5 * 1024 * 1024 * 1024) // 5 GiB
	in := InitiateUploadInput{
		TenantID:  uuid.New(),
		UserID:    uuid.New(),
		Filename:  "huge.pdf",
		SizeBytes: 10 * 1024 * 1024 * 1024, // 10 GiB
	}
	err := validateInitiate(in, standardCeiling)
	require.Error(t, err)
	require.Equal(t, vdmserr.KindPayloadTooLarge, vdmserr.KindOf(err),
		"oversized uploads must return KindPayloadTooLarge → HTTP 413 / gRPC ResourceExhausted")
	require.Contains(t, err.Error(), "exceeds tenant plan ceiling",
		"error message must name the plan ceiling so the client can render a useful 413")
}

func TestValidateInitiate_AcceptsAtExactCeiling(t *testing.T) {
	ceiling := int64(5 * 1024 * 1024 * 1024)
	in := InitiateUploadInput{
		TenantID:  uuid.New(),
		UserID:    uuid.New(),
		Filename:  "edge.pdf",
		SizeBytes: ceiling,
	}
	require.NoError(t, validateInitiate(in, ceiling))
}

// --- 2. Deny-list executable MIME + extension ---------------------------

func TestMIMEBlocklist_RejectsWindowsExe(t *testing.T) {
	require.True(t, scanner.IsBlockedMIME("application/x-msdownload"))
	require.True(t, scanner.IsBlockedMIME("application/x-dosexec"))
	require.True(t, scanner.IsBlockedMIME("application/x-executable"))
	require.False(t, scanner.IsBlockedMIME("application/pdf"))
}

func TestExtensionBlocklist_CaseInsensitive(t *testing.T) {
	require.True(t, scanner.IsBlockedExtension("malware.EXE"))
	require.True(t, scanner.IsBlockedExtension("payload.bat"))
	require.False(t, scanner.IsBlockedExtension("report.pdf"))
}

// --- 3. MIME mismatch detection — client lies about Content-Type --------
//
// Client claims image/png but sends PDF bytes. MIMEMatchesDeclared must
// return false so the finalize path increments mime_mismatch_total and
// stores the server-detected type as authoritative.

func TestMIMEMatchesDeclared_PDFClaimingPNG_IsMismatch(t *testing.T) {
	// %PDF-1.4 magic bytes at offset 0 is all filetype needs.
	pdfHead := []byte("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	detected, err := scanner.DetectFromBytes(pdfHead)
	require.NoError(t, err)
	require.True(t, detected.IsKnown, "filetype should identify PDF magic bytes")
	require.Equal(t, "application/pdf", detected.MIME)

	matches := scanner.MIMEMatchesDeclared("image/png", detected)
	require.False(t, matches, "client-declared image/png must NOT match detected application/pdf")
}

// --- 4. ClamAV fail-closed when scanner is nil (dev / outage path) ------

func TestScanBuffer_NilScanner_FailsClosed(t *testing.T) {
	s := &Service{
		log: zerolog.Nop(),
	}
	out := s.scanBuffer(context.Background(), bytes.NewReader([]byte("payload")))
	require.Equal(t, model.ScanError, out.result,
		"a nil scanner must surface ScanError so CompleteUpload can 503 fail-closed")
}

// --- 5. Scan-duration metric plumbing — guard against an import cycle
//       that would break the metrics package. If this build, the metric
//       Observe call on the nil-scanner branch already compiled.

func TestScanBuffer_CompletesQuickly_OnNilScanner(t *testing.T) {
	s := &Service{log: zerolog.Nop()}
	start := time.Now()
	_ = s.scanBuffer(context.Background(), bytes.NewReader(nil))
	require.Less(t, time.Since(start), time.Second,
		"nil-scanner path must be synchronous and fast (fail-closed is immediate)")
}
