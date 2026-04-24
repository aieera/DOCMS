package internalauth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestSetModeInfo_ExactlyOneSeries(t *testing.T) {
	// Configuring twice with different modes must not leave a stale
	// "both" series behind when we switch to "mtls".
	setModeInfo(ModeBoth)
	setModeInfo(ModeMTLS)
	got := testutil.ToFloat64(modeInfo.WithLabelValues(string(ModeMTLS)))
	if got != 1 {
		t.Fatalf("mode=mtls gauge: want 1, got %v", got)
	}
	stale := testutil.ToFloat64(modeInfo.WithLabelValues(string(ModeBoth)))
	if stale != 0 {
		t.Fatalf("mode=both must be cleared after switch, got %v", stale)
	}
}

func TestSetCertExpiry_PerSAN(t *testing.T) {
	now := time.Now().Unix()
	setCertExpiry(map[string]int64{
		"worker.temporal.internal": now + 86400,
		"sweeper.ack.internal":     now + 172800,
	})
	if got := testutil.ToFloat64(certExpiry.WithLabelValues("worker.temporal.internal")); got != float64(now+86400) {
		t.Fatalf("worker expiry: want %d, got %v", now+86400, got)
	}
	// nil map resets.
	setCertExpiry(nil)
	if got := testutil.ToFloat64(certExpiry.WithLabelValues("worker.temporal.internal")); got != 0 {
		t.Fatalf("expected reset to 0, got %v", got)
	}
}

func TestReadClientCertExpiry_Parses(t *testing.T) {
	path := writeThrowawayCert(t, []string{"foo.internal", "bar.internal"},
		time.Now().Add(-time.Hour), time.Now().Add(48*time.Hour))
	got := readClientCertExpiry(path)
	if len(got) != 2 {
		t.Fatalf("want 2 SAN keys, got %d", len(got))
	}
	for _, san := range []string{"foo.internal", "bar.internal"} {
		if got[san] == 0 {
			t.Fatalf("missing expiry for %s", san)
		}
	}
}

func TestReadClientCertExpiry_SilentOnMissingFile(t *testing.T) {
	if out := readClientCertExpiry(""); out != nil {
		t.Fatalf("empty path must return nil, got %v", out)
	}
	if out := readClientCertExpiry("/nope/does/not/exist.pem"); out != nil {
		t.Fatalf("missing file must return nil, got %v", out)
	}
}

// writeThrowawayCert emits a PEM cert to a temp file. No CA — we only
// need to verify the parser picks up DNSNames / NotAfter.
func writeThrowawayCert(t *testing.T, sans []string, notBefore, notAfter time.Time) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: strings.Join(sans, ",")},
		DNSNames:     sans,
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	dir := t.TempDir()
	path := filepath.Join(dir, "leaf.pem")
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}
