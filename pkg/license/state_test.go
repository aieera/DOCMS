package license

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// resetGlobal clears the process-wide state Init writes, so each case
// starts from an unlicensed process.
func resetGlobal(t *testing.T) {
	t.Helper()
	global = &State{}
	t.Cleanup(func() { global = &State{} })
}

// licenseEnv pins every variable Init reads. Empty means unset.
func licenseEnv(t *testing.T, environment, pubKey, token string) {
	t.Helper()
	t.Setenv("SEDOC_ENVIRONMENT", environment)
	t.Setenv("SEDOC_LICENSE_PUBLIC_KEY", pubKey)
	t.Setenv("SEDOC_LICENSE_PUBLIC_KEY_FILE", "")
	t.Setenv("SEDOC_LICENSE_JWT", token)
	t.Setenv("SEDOC_REQUIRE_LICENSE", "")
}

func newKeyPair(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	return priv, string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func signLicense(t *testing.T, priv *rsa.PrivateKey) string {
	t.Helper()
	claims := rawClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "aaaaaaaa-aaaa-7aaa-aaaa-aaaaaaaaaaaa",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(365 * 24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
		TenantName: "Acme Corp",
		SeatLimit:  10,
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(priv)
	if err != nil {
		t.Fatalf("sign license: %v", err)
	}
	return tok
}

// O-10: the embedded dev public key is a trust root only in dev. A prod
// process must neither fall back to it nor accept it as the configured key.
func TestLicense_ProdRejectsDevKey(t *testing.T) {
	priv, pub := newKeyPair(t)
	token := signLicense(t, priv)

	t.Run("dev key configured explicitly", func(t *testing.T) {
		resetGlobal(t)
		licenseEnv(t, "prod", DevPublicKeyPEM, token)
		err := Init()
		if err == nil || !strings.Contains(err.Error(), "dev public key") {
			t.Fatalf("Init() = %v, want a dev-public-key rejection", err)
		}
		if Current() != nil {
			t.Fatal("claims loaded despite the rejected trust root")
		}
	})

	t.Run("dev key configured without a license", func(t *testing.T) {
		resetGlobal(t)
		licenseEnv(t, "prod", DevPublicKeyPEM, "")
		if err := Init(); err == nil {
			t.Fatal("Init() accepted the dev public key in prod")
		}
	})

	t.Run("no key configured: no fallback to the dev key", func(t *testing.T) {
		resetGlobal(t)
		licenseEnv(t, "prod", "", token)
		err := Init()
		if err == nil || !strings.Contains(err.Error(), "SEDOC_LICENSE_PUBLIC_KEY") {
			t.Fatalf("Init() = %v, want an error naming SEDOC_LICENSE_PUBLIC_KEY", err)
		}
	})

	t.Run("staging is not dev", func(t *testing.T) {
		resetGlobal(t)
		licenseEnv(t, "staging", DevPublicKeyPEM, token)
		if err := Init(); err == nil || !strings.Contains(err.Error(), "dev public key") {
			t.Fatalf("Init() = %v, want a dev-public-key rejection in staging", err)
		}
	})

	t.Run("operator key from env verifies", func(t *testing.T) {
		resetGlobal(t)
		licenseEnv(t, "prod", pub, token)
		if err := Init(); err != nil {
			t.Fatalf("Init() = %v", err)
		}
		if c := Current(); c == nil || c.TenantName != "Acme Corp" {
			t.Fatalf("Current() = %+v, want the signed claims", c)
		}
	})

	t.Run("operator key from file verifies", func(t *testing.T) {
		resetGlobal(t)
		licenseEnv(t, "prod", "", token)
		path := filepath.Join(t.TempDir(), "license.pub.pem")
		if err := os.WriteFile(path, []byte(pub), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("SEDOC_LICENSE_PUBLIC_KEY_FILE", path)
		if err := Init(); err != nil {
			t.Fatalf("Init() = %v", err)
		}
		if Current() == nil {
			t.Fatal("claims not loaded")
		}
	})

	t.Run("prod without a license stays unlicensed unless required", func(t *testing.T) {
		resetGlobal(t)
		licenseEnv(t, "prod", "", "")
		if err := Init(); err != nil {
			t.Fatalf("Init() = %v, want unlicensed mode", err)
		}
		t.Setenv("SEDOC_REQUIRE_LICENSE", "true")
		if err := Init(); err == nil {
			t.Fatal("Init() = nil with SEDOC_REQUIRE_LICENSE=true and no license")
		}
	})
}

// The dev key stays the default trust root in dev, so local license-gen
// output keeps working without configuration.
func TestLicense_DevDefaultsToDevKey(t *testing.T) {
	priv, _ := newKeyPair(t)
	for _, env := range []string{"", "dev"} {
		resetGlobal(t)
		licenseEnv(t, env, "", signLicense(t, priv))
		// A license signed by a key other than the dev key must fail,
		// which proves the dev key (not "no key") was the trust root.
		err := Init()
		if err == nil || !strings.Contains(err.Error(), "parse") {
			t.Fatalf("SEDOC_ENVIRONMENT=%q: Init() = %v, want a signature failure against the dev key", env, err)
		}
	}
}
