//go:build pades_corpus

package pades

// Corpus-walker harness.
//
// Enabled with `-tags pades_corpus`. Reads PAdES-B-LT signed PDFs
// from the directory at `VAULTDMS_PADES_FIXTURES` (default
// `tests/fixtures/pades`) and runs `ValidatePAdESBLT` against each.
// Fails on any file that doesn't satisfy `IsValidPAdESBLT()`.
//
// CI doesn't ship real signed fixtures (needs the Java DSS sidecar
// + a certificate + TSA access). Operators run this harness once
// per release with freshly-produced fixtures — see
// `docs/runbooks/15-pades-harness.md`. In the meantime the tag is
// off so CI is green; operators flip it on for the validation run.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPAdESCorpus(t *testing.T) {
	dir := os.Getenv("VAULTDMS_PADES_FIXTURES")
	if dir == "" {
		dir = "../../../../tests/fixtures/pades"
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("fixture dir %q missing: %v; provide real signed PDFs before running -tags pades_corpus", dir, err)
	}
	found := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".pdf") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		t.Run(e.Name(), func(t *testing.T) {
			body, err := os.ReadFile(path)
			require.NoError(t, err)
			rep, err := ValidatePAdESBLT(body)
			require.NoError(t, err, "parse %s", e.Name())
			require.True(t, rep.IsValidPAdESBLT(),
				"%s failed PAdES-B-LT: sigs=%d docts=%d dss=%v whole=%v diag=%v",
				e.Name(), rep.SignatureCount, rep.DocumentTimestamps, rep.HasDSS, rep.ByteRangeCoversWholeFile, rep.Diagnostics)
		})
		found++
	}
	if found == 0 {
		t.Skipf("no .pdf fixtures under %q — populate from the signer sidecar before enabling", dir)
	}
}
