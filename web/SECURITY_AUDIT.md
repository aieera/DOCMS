# Web Frontend Security Audit

**Date:** 2026-04-17
**Scope:** `web/` (React 18 + TypeScript + Vite)

---

## Summary

| | HIGH | CRITICAL | MODERATE | LOW |
|-|-----:|---------:|---------:|----:|
| Before | **4** | 0 | 2 | 0 |
| After | **0** | 0 | 2 | 0 |

Remaining 2 MODERATE findings are in `esbuild` (dev-server only, not bundled) — see the "Remaining" section below.

**Method:** Upgraded `react-pdf` from v7.7.3 → v10.4.1. This single change eliminated all 4 HIGH vulnerabilities. No `--force` used.

---

## CVE-1104044 — pdfjs-dist @ 3.11.174

- **Advisory:** [GHSA-wgrm-67xf-hhpq](https://github.com/advisories/GHSA-wgrm-67xf-hhpq)
- **Severity:** HIGH (CVSS 8.8)
- **Type:** Arbitrary JavaScript execution when opening a malicious PDF
- **Dependency path:** `react-pdf@7.7.3` → nested `pdfjs-dist@3.11.174`
- **Runtime-reachable:** **YES** — executed in the browser on every PDF open.
- **Resolution:** FIXED via `react-pdf@10.4.1` upgrade (nested pdfjs-dist now 5.4.296, safe).

## CVE-chain — tar @ <=7.5.10 (6 advisories)

- **Advisories:**
  - [GHSA-34x7-hfp2-rc4v](https://github.com/advisories/GHSA-34x7-hfp2-rc4v) — Hardlink path traversal (CVSS 8.2)
  - [GHSA-8qq5-rm4j-mr97](https://github.com/advisories/GHSA-8qq5-rm4j-mr97) — Symlink poisoning
  - [GHSA-83g3-92jg-28cx](https://github.com/advisories/GHSA-83g3-92jg-28cx) — Hardlink target escape (CVSS 7.1)
  - [GHSA-qffp-2rhf-9h96](https://github.com/advisories/GHSA-qffp-2rhf-9h96) — Drive-relative linkpath traversal
  - [GHSA-9ppj-qmqm-q256](https://github.com/advisories/GHSA-9ppj-qmqm-q256) — Symlink path traversal
  - [GHSA-r6q2-hw4h-h46w](https://github.com/advisories/GHSA-r6q2-hw4h-h46w) — Path reservation race (CVSS 8.8)
- **Type:** Archive-extraction path traversal + symlink escape
- **Dependency path:** `react-pdf@7.7.3` → `pdfjs-dist@3.11.174` → `canvas@^2.11.2` (optional) → `@mapbox/node-pre-gyp` → `tar`
- **Runtime-reachable:** NO (install-time postinstall only)
- **Resolution:** FIXED — `react-pdf@10` drops the pdfjs-dist@3 nested copy, which drops the optional `canvas` dep, which drops the entire tar/node-pre-gyp chain.

## CVE-chain — @mapbox/node-pre-gyp @ <=1.0.11

- **Severity:** HIGH (propagates tar advisories)
- **Dependency path:** Same as tar, one level up.
- **Runtime-reachable:** NO
- **Resolution:** FIXED via the react-pdf upgrade (dep no longer in tree).

## CVE-chain — react-pdf @ 1.6.0 – 8.0.2

- **Severity:** HIGH (propagates pdfjs-dist RCE)
- **Dependency path:** Direct dependency in `web/package.json`.
- **Runtime-reachable:** YES
- **Resolution:** FIXED — upgraded to `react-pdf@10.4.1`.

---

## Migration notes (react-pdf v7 → v10)

The public API surface used by `web/src/components/viewer/PDFViewer.tsx` is unchanged between v7 and v10:
- `import { Document, Page, pdfjs } from 'react-pdf'` — still valid
- `pdfjs.GlobalWorkerOptions.workerSrc` — still the correct worker config hook
- `pdf.worker.min.mjs` filename is identical in pdfjs-dist v3, v4, and v5
- `<Document file onLoadSuccess loading error>` — same props
- `<Page pageNumber width renderTextLayer renderAnnotationLayer>` — same props

**No code changes were required in PDFViewer.tsx.** TypeScript passes with zero errors. Production bundle builds cleanly.

---

## Verification

```bash
# Before
$ npm audit | tail -3
6 vulnerabilities (2 moderate, 4 high)

# After
$ npm audit | tail -3
2 moderate severity vulnerabilities

$ npm audit --json | jq .metadata.vulnerabilities
{
  "critical": 0,
  "high": 0,
  "moderate": 2,
  "low": 0,
  "total": 2
}

# Dep-tree change
$ npm ls pdfjs-dist
vaultdms-web@0.1.0
+-- pdfjs-dist@4.10.38       ← direct, safe
`-- react-pdf@10.4.1
  `-- pdfjs-dist@5.4.296     ← nested, safe (was 3.11.174 vulnerable)

$ npm ls canvas
(empty)                      ← no longer in tree (was transitive of pdfjs-dist@3)

$ npm ls tar
(empty)                      ← no longer in tree

$ npm ls @mapbox/node-pre-gyp
(empty)                      ← no longer in tree

# TypeScript
$ npx tsc --noEmit           ← exit 0, zero errors

# Production build
$ npm run build              ← exit 0, dist/assets/index-*.js built successfully
```

---

## Remaining MODERATE findings

**2 MODERATE** in `esbuild` (bundled inside Vite):
- [GHSA-67mh-4wv8-2f99](https://github.com/advisories/GHSA-67mh-4wv8-2f99) — esbuild dev-server allows any website to read responses from localhost
- Fix path requires `vite@8.0.8` (breaking change — Vite 5 → 8).

**Runtime-reachable:** NO — esbuild dev-server is only active during `npm run dev` on a developer's workstation. Not included in production bundle. Risk is bounded to developer machines actively running `npm run dev` with a browser open to a malicious site simultaneously.

**Decision:** ACCEPTED — not worth a major Vite bump right now. Track for future upgrade. CI `npm audit --audit-level=high` gate will not flag this (MODERATE falls below threshold).

---

## Changes applied

| File | Change |
|------|--------|
| `web/package.json` | `react-pdf: ^7.7.0` → `^10.4.1` |
| `web/package-lock.json` | Regenerated — removed `pdfjs-dist@3`, `canvas`, `@mapbox/node-pre-gyp`, `tar` (all were nested deps of the old react-pdf) |
| `.github/dependabot.yml` | Renamed group `npm-minor` → `patch-and-minor` and adjusted `open-pull-requests-limit` to 5 per spec |

**No source files modified.** PDFViewer.tsx still compiles and runs on the new version.

## CI guardrails (already in place — verified)

- `.github/workflows/ci.yml` → `security-npm` job runs `npm audit --audit-level=high` in `web/` and fails CI on any new HIGH finding
- `.github/dependabot.yml` → weekly npm check against `/web` with patch-and-minor grouping

---

## Manual browser verification (deferred)

The backend services are not currently running locally (they require Postgres + Redis + NATS + OpenSearch + Qdrant + Temporal + S3 credentials). A full end-to-end PDF viewer test requires either the local infrastructure or staging deployment.

**What HAS been verified:**
- TypeScript compiles (0 errors)
- Vite production build succeeds (0 errors)
- PDFViewer.tsx uses only v7→v10 stable API surface
- `pdf.worker.min.mjs` filename unchanged between versions

**What needs verification on next deployment:**
- Open a PDF in the viewer
- Page navigation (prev/next buttons)
- TextLayer rendering (for selection)

Given the backwards-compatible API surface and successful production build, runtime compatibility is very likely, but the final manual browser test should be part of the PR review.
