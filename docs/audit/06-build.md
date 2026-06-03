# SeDoc Audit — Phase E: Build Verification

**Date:** 2026-04-16

No files were modified. All findings are captured as-is.

---

## 1. Go Build (`go build ./...`)

### Modules that compile cleanly (8):

| Module | Result |
|--------|--------|
| pkg/ | ✓ PASS |
| services/audit | ✓ PASS |
| services/billing | ✓ PASS |
| services/connector | ✓ PASS |
| services/notification | ✓ PASS |
| services/search | ✓ PASS |
| services/signature | ✓ PASS |
| services/workflow | ✓ PASS |

### Modules that FAIL to compile (4):

| Module | Error | File:Line |
|--------|-------|-----------|
| **services/auth** | `"encoding/json" imported and not used` | `internal/scim/repo.go:5` |
| **services/document** | `does not contain package vaultdms/v1` (proto gen missing) | `cmd/server/main.go:31` |
| **services/policy** | `does not contain package vaultdms/v1` (proto gen missing) | `cmd/server/main.go:25` |
| **services/storage** | `does not contain package vaultdms/v1` (proto gen missing) | `cmd/server/main.go:28` |

### Not in go.work (cannot build):

| Module | Reason |
|--------|--------|
| cmd/dms-admin | Not listed in go.work |

**Summary: 8 PASS, 4 FAIL (1 unused import, 3 missing proto gen)**

---

## 2. Go Vet (`go vet ./...`)

| Module | Findings |
|--------|----------|
| pkg/ | 0 |
| services/auth | 1: `internal/scim/repo.go:5: "encoding/json" imported and not used` |
| services/document | blocked by proto gen |
| services/policy | blocked by proto gen |
| services/storage | blocked by proto gen |
| All others (7) | 0 |

---

## 3. golangci-lint

**Not installed on this machine.** `.golangci.yml` exists at repo root. CI pipeline (`ci.yml`) runs it. Cannot verify locally.

---

## 4. buf lint (Proto)

**26 findings:**

| Category | Count | Example |
|----------|-------|---------|
| Missing google.api.http import | 24 | `document.proto:219:13: cannot find google.api.http in this scope` |
| Duplicate message name | 1 | `collaboration.proto:54:9: ShareLink declared multiple times` |
| Missing import file | 1 | `document.proto:10:1: imported file does not exist` |

**Root causes:**
- `document.proto` imports `google/api/annotations.proto` for HTTP annotations, but buf.yaml doesn't declare the `googleapis` dependency
- `collaboration.proto` defines `ShareLink` which conflicts with `document.proto`'s `ShareLink` (should be namespaced or one removed)

---

## 5. Docker Compose Config

```
docker compose config --quiet → exit code 0
```

**docker-compose.yml is valid.** All 13 services parse correctly.

---

## 6. Python Services

**Python 3 is NOT installed on this machine.** Cannot run `py_compile`, `ruff`, or `mypy`.

| Service | .py files | Status |
|---------|-----------|--------|
| intelligence | 30 | **UNTESTED** |
| preview | 27 | **UNTESTED** |

**Note:** Both services have `requirements.txt` and `Dockerfile`. Syntax can be verified inside Docker build.

---

## 7. Web Frontend

### TypeScript compilation:

```
npx tsc --noEmit → exit code 0 (0 errors)
```

**All 107 TypeScript files compile cleanly in strict mode.**

### npm audit:

```
6 vulnerabilities (2 moderate, 4 high)
```

| Severity | Count | Source |
|----------|-------|-------|
| HIGH | 4 | `@mapbox/node-pre-gyp` (transitive dep of `canvas`/`pdfjs-dist`) |
| MODERATE | 2 | Same dependency tree |

These are in transitive dependencies, not direct code. Fixable with `npm audit fix --force` or pinning.

### ESLint:

Not configured separately (TypeScript strict mode serves as the primary check).

---

## 8. Collaboration Service (Node.js)

```
node --check src/index.js → exit 0
node --check src/redis.js → exit 0
node --check src/connections.js → exit 0
node --check src/handler.js → exit 0
```

**All 4 JavaScript files pass syntax validation.**

---

## 9. Mobile App

Not verified (Expo requires `npx expo` which needs `npm install` first, and React Native compilation requires platform SDKs).

---

## Summary

| Target | Result | Blockers |
|--------|--------|----------|
| Go pkg/ | ✓ PASS | — |
| Go services (8 of 11) | ✓ PASS | — |
| Go services/auth | **FAIL** | Unused import `scim/repo.go:5` |
| Go services/document | **FAIL** | Proto gen missing |
| Go services/policy | **FAIL** | Proto gen missing |
| Go services/storage | **FAIL** | Proto gen missing |
| Go cmd/dms-admin | **FAIL** | Not in go.work |
| Proto (buf lint) | **26 errors** | Missing googleapis dep, duplicate ShareLink |
| docker-compose.yml | ✓ VALID | — |
| Python (intelligence) | **UNTESTED** | Python not installed |
| Python (preview) | **UNTESTED** | Python not installed |
| Web TypeScript | ✓ PASS (0 errors) | — |
| Web npm audit | **6 vulns** (4 high) | Transitive deps |
| Collaboration Node.js | ✓ PASS | — |
| Mobile | **UNTESTED** | Requires Expo SDK |

**Total build failures: 4 Go modules + 26 proto lint errors**
**Root cause for 3 of 4 Go failures: proto codegen never ran**
