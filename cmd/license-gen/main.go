// license-gen — VaultDMS license JWT signer (ADR 0095).
//
// Usage:
//   license-gen \
//     --key       deploy/license/dev.priv.pem \
//     --tenant-id 00000000-0000-0000-0000-000000000001 \
//     --tenant-name "Acme Corp" \
//     --seat-limit 250 \
//     --expires    2027-04-19 \
//     --features   esign,mcp,intel_llm \
//     --connectors google_workspace,salesforce \
//     --regions    us-east-1,eu-west-1 \
//     --issued-to  admin@acme.example \
//     --out        acme.license.jwt
//
// In prod this binary is run only by BD ops with the private key.
// The dev key (deploy/license/dev.priv.pem) is gitignored; the
// matching public key (dev.pub.pem) is embedded in pkg/license/
// dev_pubkey.go so every service verifies against the same key.
package main

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type featureFlags struct {
	Esign      bool     `json:"esign,omitempty"`
	MCP        bool     `json:"mcp,omitempty"`
	IPaaS      bool     `json:"ipaas,omitempty"`
	IntelLLM   bool     `json:"intel_llm,omitempty"`
	Connectors []string `json:"connectors,omitempty"`
	Regions    []string `json:"regions,omitempty"`
}

type licenseClaims struct {
	jwt.RegisteredClaims
	TenantName   string       `json:"tenant_name"`
	SeatLimit    int          `json:"seat_limit"`
	FeatureFlags featureFlags `json:"feature_flags"`
	IssuedTo     string       `json:"issued_to"`
	IssuedBy     string       `json:"issued_by"`
	GraceDays    int          `json:"grace_days"`
}

func main() {
	keyPath := flag.String("key", "deploy/license/dev.priv.pem", "RSA private key (PEM)")
	tenantID := flag.String("tenant-id", "", "tenant UUID (license sub)")
	tenantName := flag.String("tenant-name", "", "tenant display name")
	seatLimit := flag.Int("seat-limit", 100, "max active users")
	expiresStr := flag.String("expires", "", "expiry date (YYYY-MM-DD)")
	featuresStr := flag.String("features", "esign,mcp,intel_llm", "comma-sep feature flags")
	connectorsStr := flag.String("connectors", "google_workspace,salesforce,m365", "comma-sep connector allow-list")
	regionsStr := flag.String("regions", "us-east-1,eu-west-1", "comma-sep region allow-list")
	issuedTo := flag.String("issued-to", "admin@example.com", "license recipient email")
	issuedBy := flag.String("issued-by", "bd-ops@vaultdms", "license issuer email")
	graceDays := flag.Int("grace-days", 30, "days of read-only grace after expiry")
	outPath := flag.String("out", "", "output file path (default: stdout)")
	flag.Parse()

	if *tenantID == "" || *tenantName == "" || *expiresStr == "" {
		log.Fatal("license-gen: --tenant-id, --tenant-name, --expires are required")
	}

	expiry, err := time.Parse("2006-01-02", *expiresStr)
	if err != nil {
		log.Fatalf("license-gen: bad --expires %q: %v", *expiresStr, err)
	}

	priv, err := loadPrivateKey(*keyPath)
	if err != nil {
		log.Fatalf("license-gen: %v", err)
	}

	features := splitFeatures(*featuresStr)
	claims := &licenseClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "vaultdms-bd-ops",
			Subject:   *tenantID,
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(expiry),
		},
		TenantName: *tenantName,
		SeatLimit:  *seatLimit,
		FeatureFlags: featureFlags{
			Esign:      features["esign"],
			MCP:        features["mcp"],
			IPaaS:      features["ipaas"],
			IntelLLM:   features["intel_llm"],
			Connectors: splitCSV(*connectorsStr),
			Regions:    splitCSV(*regionsStr),
		},
		IssuedTo:  *issuedTo,
		IssuedBy:  *issuedBy,
		GraceDays: *graceDays,
	}

	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := tok.SignedString(priv)
	if err != nil {
		log.Fatalf("license-gen: sign: %v", err)
	}

	if *outPath == "" {
		fmt.Println(signed)
		return
	}
	if err := os.WriteFile(*outPath, []byte(signed), 0o600); err != nil {
		log.Fatalf("license-gen: write %s: %v", *outPath, err)
	}
	fmt.Fprintf(os.Stderr, "license-gen: wrote %s (expires %s)\n", *outPath, expiry.Format("2006-01-02"))
}

func loadPrivateKey(path string) (*rsa.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, fmt.Errorf("pem decode failed")
	}
	// PKCS#1 first, then PKCS#8 fallback
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	rsaKey, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is not RSA")
	}
	return rsaKey, nil
}

func splitFeatures(s string) map[string]bool {
	out := map[string]bool{}
	for _, f := range splitCSV(s) {
		out[f] = true
	}
	return out
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
