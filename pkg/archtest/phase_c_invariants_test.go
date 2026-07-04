package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Phase C invariants — already satisfied at the commit that landed this
// file, per prior waves (6.1–6.5). These tests exist to keep them
// satisfied. If any fails on a future PR, fix the regression before
// merging; do not "fix" it by loosening the predicate.

// --- C2 — no auth token in localStorage / sessionStorage ----------

func TestPhaseC2_NoAuthTokenInBrowserStorage(t *testing.T) {
	webSrc := filepath.Join(findRepoRoot(t), "web", "src")

	// Match any *.ts / *.tsx file reading the browser storage APIs
	// with a key that looks like session / token / auth. Positive
	// matches must be investigated — browser storage is not allowed
	// to hold the session token per §8.1 / C2.
	re := regexp.MustCompile(`(localStorage|sessionStorage)\s*\.\s*(getItem|setItem|removeItem)\s*\(\s*['"][^'"]*(token|bearer|session|auth)[^'"]*['"]`)

	hits := grepTree(t, webSrc, ".ts", ".tsx", re)
	if len(hits) > 0 {
		t.Fatalf("C2 regression — auth-like key(s) found in browser storage (per §8.1, session MUST be cookie-only):\n  %s",
			strings.Join(hits, "\n  "))
	}
}

// TestPhaseC2_NoAuthTokenInMobileInsecureStorage is the §8.1 analogue
// for the mobile app (ADR 0117): the session token lives in
// expo-secure-store (Keychain / EncryptedSharedPreferences), NEVER in
// AsyncStorage — which is a plaintext file readable on a rooted device
// or from a backup. AsyncStorage is fine for the display cache
// (react-query persister); it must not hold anything auth-shaped.
func TestPhaseC2_NoAuthTokenInMobileInsecureStorage(t *testing.T) {
	mobileDir := filepath.Join(findRepoRoot(t), "mobile")

	re := regexp.MustCompile(`AsyncStorage\s*\.\s*(getItem|setItem|removeItem|mergeItem)\s*\(\s*['"][^'"]*(token|bearer|session|auth|credential|password)[^'"]*['"]`)

	hits := grepTree(t, mobileDir, ".ts", ".tsx", re)
	if len(hits) > 0 {
		t.Fatalf("C2 regression (mobile) — auth-like key(s) found in AsyncStorage (per §8.1/ADR 0117, the session lives in expo-secure-store only):\n  %s",
			strings.Join(hits, "\n  "))
	}

	// The literal-key regex can't see a constant-keyed call
	// (setItemAsync(KEY_TOKEN, …)), so additionally pin the token
	// store itself: the file that persists the session must not
	// reference AsyncStorage at all — swapping SecureStore out for
	// AsyncStorage there is the exact regression this test exists
	// to catch.
	authStore := filepath.Join(mobileDir, "store", "authStore.ts")
	if b, err := os.ReadFile(authStore); err == nil {
		// Match the IMPORT, not the word — the file's own doc comment
		// legitimately says "never touches AsyncStorage".
		if strings.Contains(string(b), "@react-native-async-storage") {
			t.Fatalf("C2 regression (mobile) — %s imports AsyncStorage; the session store must use expo-secure-store only", authStore)
		}
	}
}

// --- C3 — no math/rand in SAML signer ------------------------------

func TestPhaseC3_NoMathRandInSAMLSigner(t *testing.T) {
	samlDir := filepath.Join(findRepoRoot(t), "services", "auth", "internal", "sso")

	// Only the IMPORT path matters for the regression. Comments and
	// tests that reference "math/rand" for documentation are fine —
	// this regex intentionally matches `"math/rand"` (with quotes),
	// i.e. an import line.
	re := regexp.MustCompile(`"math/rand"`)

	// But do include the import if it sits inside an actual `import`
	// block (vs a comment/test doc-string). Simpler: exclude _test.go
	// and skip lines that start with `//` after trimming.
	hits := grepTreeFiltered(t, samlDir, []string{".go"}, re, func(path, line string) bool {
		if strings.HasSuffix(path, "_test.go") {
			return false
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") {
			return false
		}
		return true
	})
	if len(hits) > 0 {
		t.Fatalf("C3 regression — math/rand imported in SAML package (§8.1 requires crypto/rand for cert serials):\n  %s",
			strings.Join(hits, "\n  "))
	}
}

// --- C4 — no context.Background() in handler / consumer files -----

func TestPhaseC4_NoContextBackgroundInHandlers(t *testing.T) {
	servicesDir := filepath.Join(findRepoRoot(t), "services")

	re := regexp.MustCompile(`context\.Background\(\)`)

	// Only files whose path contains /handler/ or /consumer/ or ends
	// in _consumer.go / _handler.go. Service code that spins up a
	// background goroutine at boot is still allowed to use
	// context.Background in cmd/server/main.go.
	hits := grepTreeFiltered(t, servicesDir, []string{".go"}, re, func(path, line string) bool {
		if strings.HasSuffix(path, "_test.go") {
			return false
		}
		lp := strings.ToLower(path)
		if !(strings.Contains(lp, "/handler/") || strings.Contains(lp, "/consumer/") ||
			strings.HasSuffix(lp, "_handler.go") || strings.HasSuffix(lp, "_consumer.go") ||
			strings.HasSuffix(lp, "nats_consumer.go")) {
			return false
		}
		// Same comment/doc skip as C3.
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") {
			return false
		}
		return true
	})
	if len(hits) > 0 {
		t.Fatalf("C4 regression — context.Background() found in a handler/consumer file (§15.3: request-scoped ctx):\n  %s",
			strings.Join(hits, "\n  "))
	}
}

// --- C5 — no direct NATS publish from service code ----------------

func TestPhaseC5_NoDirectNATSPublish(t *testing.T) {
	servicesDir := filepath.Join(findRepoRoot(t), "services")

	// (?i): receivers are fields as often as locals (a.JS, p.Nats) —
	// the earlier case-sensitive form let `JS.Publish` slip through,
	// which is exactly how the ADR 0085 emit bypassed this test.
	re := regexp.MustCompile(`(?i)\b(nats|nc|js)\.Publish(?:Msg)?\s*\(`)

	hits := grepTreeFiltered(t, servicesDir, []string{".go"}, re, func(path, line string) bool {
		if strings.HasSuffix(path, "_test.go") {
			return false
		}
		// The outbox publisher in pkg/database legitimately calls
		// js.PublishMsg; services MUST go through the outbox instead.
		// pkg/ is not under services/, so this filter is belt-and-suspenders.
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") {
			return false
		}
		// ADR 0077 — the per-tenant eventstream mirror forwards
		// JetStream→JetStream (`dms.*` → `tenant.{id}.events.*`). There
		// is no DB commit in between, so the outbox isn't applicable;
		// upstream Nak on publish-failure gives the same at-least-once
		// guarantee. Whitelist the package the same way pkg/database
		// is whitelisted upstream by living outside services/.
		if strings.Contains(filepath.ToSlash(path), "/internal/eventstream/") {
			return false
		}
		// Same rationale for the SIEM forwarder's DLQ park: a NATS
		// consumer that fails to deliver to an external sink republishes
		// the poisoned message JetStream→JetStream (…_DLQ stream). There
		// is no DB commit in that path, so the outbox isn't applicable.
		if strings.Contains(filepath.ToSlash(path), "/internal/siem/") {
			return false
		}
		return true
	})
	if len(hits) > 0 {
		t.Fatalf("C5 regression — direct NATS publish found in services/ (§4.7: outbox-only):\n  %s",
			strings.Join(hits, "\n  "))
	}
}

// ---- helpers -------------------------------------------------------

// grepTree walks a directory tree and returns "path:linenum:line" for
// every line in every file matching an extension and the regex.
func grepTree(t *testing.T, root string, extsAndRe ...any) []string {
	t.Helper()
	var exts []string
	var re *regexp.Regexp
	for _, a := range extsAndRe {
		switch v := a.(type) {
		case string:
			exts = append(exts, v)
		case *regexp.Regexp:
			re = v
		}
	}
	if re == nil {
		t.Fatal("grepTree requires a *regexp.Regexp arg")
	}
	return grepTreeFiltered(t, root, exts, re, nil)
}

// grepTreeFiltered adds a per-line filter so callers can exclude
// comments or specific paths. When filter is nil, every matching line
// is reported.
func grepTreeFiltered(t *testing.T, root string, exts []string, re *regexp.Regexp, filter func(path, line string) bool) []string {
	t.Helper()
	var hits []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		matched := false
		for _, e := range exts {
			if strings.HasSuffix(path, e) {
				matched = true
				break
			}
		}
		if !matched {
			return nil
		}
		// Skip generated / vendor.
		if strings.Contains(path, "node_modules") || strings.Contains(path, ".pb.go") || strings.Contains(path, "routeTree.gen") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for i, line := range strings.Split(string(data), "\n") {
			if !re.MatchString(line) {
				continue
			}
			if filter != nil && !filter(path, line) {
				continue
			}
			hits = append(hits, strings.TrimSpace(line)+"  ["+path+":"+itoa(i+1)+"]")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return hits
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := []byte{}
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}
