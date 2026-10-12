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

	// Unset SEDOC_ENVIRONMENT is NOT dev: compose customer installs, ad-hoc
	// boxes and a Helm release with an empty value all land here, and the
	// dev key must not be their trust root.
	t.Run("unset env: no fallback to the dev key", func(t *testing.T) {
		resetGlobal(t)
		licenseEnv(t, "", "", token)
		unsetEnv(t, "SEDOC_ENVIRONMENT")
		// With the dev key as trust root this would be a signature
		// ("parse") failure; without any trust root it names the config.
		err := Init()
		if err == nil || !strings.Contains(err.Error(), "SEDOC_LICENSE_PUBLIC_KEY") {
			t.Fatalf("Init() = %v, want an error naming SEDOC_LICENSE_PUBLIC_KEY", err)
		}
	})

	t.Run("unset env: dev key configured explicitly is rejected", func(t *testing.T) {
		resetGlobal(t)
		licenseEnv(t, "", DevPublicKeyPEM, token)
		unsetEnv(t, "SEDOC_ENVIRONMENT")
		if err := Init(); err == nil || !strings.Contains(err.Error(), "dev public key") {
			t.Fatalf("Init() = %v, want a dev-public-key rejection", err)
		}
	})

	t.Run("unset env with REQUIRE_LICENSE and no key is an error", func(t *testing.T) {
		resetGlobal(t)
		licenseEnv(t, "", "", "")
		unsetEnv(t, "SEDOC_ENVIRONMENT")
		t.Setenv("SEDOC_REQUIRE_LICENSE", "true")
		if err := Init(); err == nil {
			t.Fatal("Init() = nil with SEDOC_REQUIRE_LICENSE=true, unset environment and no license")
		}
	})

	t.Run("unset env without a license stays unlicensed", func(t *testing.T) {
		resetGlobal(t)
		licenseEnv(t, "", "", "")
		unsetEnv(t, "SEDOC_ENVIRONMENT")
		if err := Init(); err != nil {
			t.Fatalf("Init() = %v, want unlicensed mode (local dev without a license)", err)
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

// The dev key is the default trust root only with SEDOC_ENVIRONMENT=dev
// set explicitly, so local license-gen output keeps working there.
func TestLicense_DevDefaultsToDevKey(t *testing.T) {
	priv, _ := newKeyPair(t)
	resetGlobal(t)
	licenseEnv(t, "dev", "", signLicense(t, priv))
	// A license signed by a key other than the dev key must fail with a
	// signature error, which proves the dev key (not "no key") was the
	// trust root.
	err := Init()
	if err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("Init() = %v, want a signature failure against the dev key", err)
	}
}

// unsetEnv removes key for the rest of the test (t.Setenv restores the
// original value afterwards).
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
}
