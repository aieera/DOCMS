# SeDoc Audit — Phase F: Test Coverage

**Date:** 2026-04-16

---

## 1. Go Test Results

### pkg/ (17 packages)

| Package | Tests | Pass/Fail | Coverage | Notes |
|---------|-------|-----------|----------|-------|
| pkg/dlp | 5 tests | **ALL PASS** | **84.0%** | Best coverage in the repo |
| pkg/storage | 1 test | **ALL PASS** | **10.7%** | S3 credential validation only |
| pkg/database | 1 test file | **SKIPPED** | 0% | `//go:build integration` — requires Docker |
| pkg/auth | 0 | — | 0% | No tests |
| pkg/config | 0 | — | 0% | No tests |
| pkg/crypto | 0 | — | 0% | No tests |
| pkg/errors | 0 | — | 0% | No tests |
| pkg/events | 0 | — | 0% | No tests |
| pkg/gateway | 0 | — | 0% | No tests |
| pkg/health | 0 | — | 0% | No tests |
| pkg/logger | 0 | — | 0% | No tests |
| pkg/metrics | 0 | — | 0% | No tests |
| pkg/middleware | 0 | — | 0% | No tests |
| pkg/tenant | 0 | — | 0% | No tests |
| pkg/testutil | 0 | — | 0% | Test helpers, not tested themselves |
| pkg/tracing | 0 | — | 0% | No tests |
| pkg/validation | 0 | — | 0% | No tests |

### Go Services

| Service | Test files | Tests | Pass/Fail | Coverage | Notes |
|---------|-----------|-------|-----------|----------|-------|
| **search** | 3 | 12 | **ALL PASS** | handler: **38.9%**, opensearch: **41.5%** | Best-tested service |
| **auth** | 1 | 9 | **ALL PASS** | service: **9.9%** | Unit tests for validation + bcrypt |
| **document** | 3 | — | **CANNOT RUN** | — | `//go:build integration` + proto gen missing |
| **policy** | 1 | — | **CANNOT RUN** | — | Proto gen missing blocks build |
| audit | 0 | — | — | 0% | **ZERO TESTS** |
| billing | 0 | — | — | 0% | **ZERO TESTS** |
| connector | 0 | — | — | 0% | **ZERO TESTS** |
| notification | 0 | — | — | 0% | **ZERO TESTS** |
| signature | 0 | — | — | 0% | **ZERO TESTS** |
| storage | 0 | — | — | 0% | **ZERO TESTS** |
| workflow | 0 | — | — | 0% | **ZERO TESTS** |

### Test Detail — Passing Tests

**search/internal/handler (5 tests):**
- TestSearchEndpoint_RequiresHeaders ✓
- TestAutocompleteEndpoint_RequiresQ ✓
- TestSavedSearchEndpoint_ValidatesBody ✓
- TestDeleteSavedSearch_RequiresID ✓
- TestSplitHeader ✓

**search/internal/opensearch (7 tests):**
- TestBuildSearchQuery_AlwaysIncludesReadableByFilter ✓
- TestBuildSearchQuery_PermissionFilterCannotBeOmitted ✓
- TestBuildSearchQuery_FacetsIncluded ✓
- TestBuildSearchQuery_AllFilters ✓
- TestBuildSearchQuery_EmptyQueryUsesMatchAll ✓
- TestBuildSearchQuery_SortOptions ✓
- TestPageTokenRoundtrip ✓
- TestBuildAutocompleteQuery_HasSecurityFilter ✓
- TestTemplateJSON_IsValidJSON ✓
- TestTemplateJSON_HasReadableByField ✓
- TestTemplateJSON_HasAutocompleteAnalyzer ✓
- TestTemplateJSON_StrictDynamic ✓

**auth/internal/service (9 tests):**
- TestValidateEmail ✓
- TestValidatePassword ✓
- TestValidateDisplayName ✓
- TestRandomTokenDistinct ✓
- TestBcryptRoundTrip ✓ (0.66s)
- TestRecoveryCodesDeterministicFormat ✓
- TestConsumeRecoveryCode ✓
- TestHasScope ✓
- TestSha256HexStable ✓

**pkg/dlp (5 tests):**
- TestDefaultRules_DetectCreditCard ✓
- TestDefaultRules_DetectSSN ✓
- TestCustomKeywords ✓
- TestCleanText ✓
- TestMaskValue ✓

---

## 2. Integration Tests (require Docker)

| File | Build tag | Requires |
|------|-----------|----------|
| `pkg/database/outbox_test.go` | `//go:build integration` | Postgres via testcontainers |
| `services/document/internal/service/document_service_test.go` | `//go:build integration` | Postgres + Redis via testcontainers |

These are NOT run in the standard `go test` pipeline. They require:
```bash
go test -tags integration ./...
```
with Docker running. **Not run during this audit** as Docker daemon access is limited.

---

## 3. Python Tests (cannot run — Python not installed)

### Intelligence service (4 test files):

| File | Tests (inferred from source) |
|------|-----|
| test_classify.py | 4 tests: invoice/contract/resume keywords, no-match low confidence |
| test_ner.py | 7 tests: SSN, credit card Luhn, email, phone, DOB, no false positives |
| test_extract.py | 3 tests: invoice regex, partial match, no match |
| test_duplicate.py | 5 tests: shingles, MinHash similarity, SimHash, Hamming |

**Estimated: 19 tests** — all unit tests, no external deps needed.

### Preview service (7 test files):

| File | Tests (inferred) |
|------|------|
| test_image.py | 3 tests: PNG thumbnail, EXIF strip, corrupt image |
| test_text.py | 3 tests: Python snippet, truncation, JSON MIME |
| test_pdf.py | 2 tests: PDF render (requires poppler), missing PDF |
| test_office.py | 2 tests: DOCX roundtrip (requires soffice), bogus binary |
| test_video.py | 2 tests: synthetic video (requires ffmpeg), nonexistent |
| test_email.py | 2 tests: EML parse, corrupt MSG |
| test_dispatch.py | 2 tests: MIME dispatch table, unknown MIME |

**Estimated: 16 tests** — some require system binaries (poppler, soffice, ffmpeg).

---

## 4. Frontend Tests

| Platform | Test files | Status |
|----------|-----------|--------|
| Web (React) | **0** | No test files exist |
| Mobile (React Native) | **0** | No test files exist |

---

## 5. Services with ZERO tests

| Service | .go files | Reason |
|---------|-----------|--------|
| audit | 5 | Never written |
| billing | 9 | Never written |
| connector | 11 | Never written |
| notification | 5 | Never written |
| signature | 5 | Never written |
| storage | 15 | Never written |
| workflow | 7 | Never written |

**7 of 11 Go services have zero test coverage.**

---

## 6. Summary

| Metric | Value |
|--------|-------|
| Total Go test files | 8 |
| Total Go tests passing | **26** |
| Total Go tests failing | **0** |
| Total Go tests blocked (proto gen) | ~10 (document + policy) |
| Go services with tests | **3** (search, auth, document*) |
| Go services with ZERO tests | **7** |
| pkg packages with tests | **2** (dlp, storage) |
| pkg packages with ZERO tests | **15** |
| Python test files | 11 (not runnable) |
| Frontend test files | **0** |
| Integration tests (Docker required) | 2 files |
| **Overall Go test coverage estimate** | **<5%** |
| Highest coverage package | pkg/dlp: 84% |
| Best-tested service | search: ~40% on tested packages |
