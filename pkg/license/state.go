package license

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// State is the process-wide cached license state. nil claims = unlicensed
// dev mode (today's default). Reads are lock-free via atomic.Value.
type State struct {
	claims atomic.Pointer[Claims]
	pubKey *rsa.PublicKey
	token  string // re-validated against pubKey hourly
}

var global = &State{}

// errNoTrustRoot means no public key was configured outside dev. It is
// fatal only when there is a license to verify (or one is required).
var errNoTrustRoot = errors.New("license: no trust root configured; set SEDOC_LICENSE_PUBLIC_KEY or SEDOC_LICENSE_PUBLIC_KEY_FILE")

// isDevEnvironment mirrors pkg/config, whose `environment` defaults to
// "dev" when SEDOC_ENVIRONMENT is unset.
func isDevEnvironment() bool {
	env := strings.TrimSpace(os.Getenv("SEDOC_ENVIRONMENT"))
	return env == "" || env == "dev"
}

// trustRoot resolves the public key licenses are verified against:
// SEDOC_LICENSE_PUBLIC_KEY (PEM), else SEDOC_LICENSE_PUBLIC_KEY_FILE (path
// to a PEM file), else — in dev only — the embedded DevPublicKeyPEM.
// Outside dev the dev key is refused even when configured explicitly:
// its private half is a developer convenience, not a release secret (O-10).
func trustRoot() (*rsa.PublicKey, error) {
	dev := isDevEnvironment()
	pemStr := os.Getenv("SEDOC_LICENSE_PUBLIC_KEY")
	if pemStr == "" {
		if path := os.Getenv("SEDOC_LICENSE_PUBLIC_KEY_FILE"); path != "" {
			b, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("license: read SEDOC_LICENSE_PUBLIC_KEY_FILE: %w", err)
			}
			pemStr = string(b)
		}
	}
	if pemStr == "" {
		if !dev {
			return nil, errNoTrustRoot
		}
		pemStr = DevPublicKeyPEM
	}
	pub, err := ParsePublicKeyPEM(pemStr)
	if err != nil {
		return nil, err
	}
	if !dev {
		devPub, err := ParsePublicKeyPEM(DevPublicKeyPEM)
		if err != nil {
			return nil, err
		}
		if pub.Equal(devPub) {
			return nil, errors.New("license: the embedded dev public key is only accepted with SEDOC_ENVIRONMENT=dev")
		}
	}
	return pub, nil
}

// Init resolves the trust root (see trustRoot) and loads the license (if
// any) from SEDOC_LICENSE_JWT env or /etc/vaultdms/license.jwt file.
// Returns an error when a license IS present but invalid, or when the
// configured trust root is unusable. Absent license is not an error —
// the service runs in StatusUnlicensedDev.
//
// SEDOC_REQUIRE_LICENSE=true upgrades absent-license to an error,
// for production deployments where running unlicensed is a misconfig.
func Init() error {
	pub, keyErr := trustRoot()
	if keyErr != nil && !errors.Is(keyErr, errNoTrustRoot) {
		// A configured key that is unreadable, malformed or the dev key
		// outside dev is a misconfiguration even without a license.
		return keyErr
	}
	global.pubKey = pub

	token := os.Getenv("SEDOC_LICENSE_JWT")
	if token == "" {
		// fallback: read from file
		if b, err := os.ReadFile("/etc/vaultdms/license.jwt"); err == nil {
			token = strings.TrimSpace(string(b))
		}
	}
	if token == "" {
		if os.Getenv("SEDOC_REQUIRE_LICENSE") == "true" {
			return fmt.Errorf("license: SEDOC_REQUIRE_LICENSE=true but no SEDOC_LICENSE_JWT or /etc/vaultdms/license.jwt: %w", os.ErrNotExist)
		}
		slog.Info("license: running unlicensed_dev_mode (no SEDOC_LICENSE_JWT)")
		return nil
	}
	if keyErr != nil {
		return keyErr
	}

	c, err := Verify(token, pub)
	if err != nil {
		return err
	}
	global.token = token
	global.claims.Store(c)
	slog.Info("license: loaded",
		"tenant", c.TenantName,
		"sub", c.Subject,
		"expires", c.ExpiresAt.Format(time.RFC3339),
		"status", c.DerivedStatus(time.Now()))
	return nil
}

// Current returns the most recently validated Claims. nil = unlicensed.
// Lock-free read, safe to call from any goroutine.
func Current() *Claims {
	return global.claims.Load()
}

// Status convenience wrapper.
func CurrentStatus() Status {
	return Current().DerivedStatus(time.Now())
}

// StartReloader spins a background goroutine that re-verifies the cached
// token every hour. Two reasons we re-verify rather than just checking
// `exp`: (1) the public key could rotate via release, but we don't pull
// a new key here — that's a restart-required operation; (2) re-running
// Verify catches expiry-past-grace as a fatal error rather than silently
// flipping to StatusExpired. The goroutine exits when ctx is cancelled.
func StartReloader(ctx context.Context) {
	if global.token == "" {
		return // nothing to re-validate in unlicensed dev mode
	}
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				c, err := Verify(global.token, global.pubKey)
				if err != nil {
					slog.Error("license: hourly re-validate failed", "err", err)
					// Don't crash: keep the last-known-good claims.
					// A service refusing to run on expiry is a startup
					// concern; mid-flight we'd rather degrade than die.
					continue
				}
				global.claims.Store(c)
				slog.Debug("license: re-validated", "status", c.DerivedStatus(time.Now()))
			}
		}
	}()
}
