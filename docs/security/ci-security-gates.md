# CI security gates

Four automated checks stand between a branch and `main`. Any one of
them can block a merge. This doc is the operator-facing cheat sheet:
what each gate does, what a failing gate means, and how to unblock.

The architectural rationale for this posture is in
[ADR 0033](../adr/0033-security-ci-gates.md).

## Gate 1 — SAST (Semgrep)

**Workflow:** [`.github/workflows/sast-semgrep.yml`](../../.github/workflows/sast-semgrep.yml)
**Triggers:** every PR + push to `main`.
**Fails the build on:** any ERROR-level finding.

Rulepacks:

| Pack                        | What it catches                                                 |
|-----------------------------|-----------------------------------------------------------------|
| `p/golang`                  | Idiomatic Go footguns (unchecked errors, unsafe casts, ...)     |
| `p/owasp-top-ten`           | OWASP A01–A10 patterns across Go/JS/Python                      |
| `p/jwt`                     | JWT handling (missing alg validation, `alg=none`, weak secrets) |
| `p/sql-injection`           | String-concat / sprintf SQL across languages                    |
| `p/secrets`                 | Hard-coded credentials (defence-in-depth to gitleaks)           |
| `.semgrep/*` (custom)       | VaultDMS-specific rules — see below                             |

Custom rules:

- `vaultdms-direct-xff-read` / `vaultdms-direct-xrealip-read` — bans
  direct `X-Forwarded-For` / `X-Real-IP` reads outside `pkg/trustedproxy`.
  ADR 0031 / T-D-2.
- `vaultdms-direct-auth-tenant-header` / `vaultdms-direct-user-id-header`
  — bans direct reads of `X-Auth-*` headers in handlers. Identity
  comes from `auth.User(ctx)`.
- `vaultdms-no-http-defaultclient` — backup to the forbidigo rule in
  `.golangci.yml`. T-D-5.
- `vaultdms-no-select-star-in-sql-string` — warns on `SELECT *` in
  Go string literals (excluded in migrations + tests).
- `vaultdms-no-sprintf-for-sql` — fails on `fmt.Sprintf` composing
  SQL. Use pgx `$1/$2/...` parameters.

### When it fires

1. Open the SARIF artifact on the failing run (Actions → run →
   `semgrep-sarif` artifact). Each rule ID links to Semgrep
   documentation.
2. Fix the finding. For custom rules, the rule body includes the
   canonical fix pattern.
3. **Suppressing is the last resort.** If a finding is a true false
   positive, add a file-scoped comment: `// nosemgrep: <rule-id>`
   ON THE LINE ABOVE the offending expression. Never suppress at
   the directory level.

## Gate 2 — Dependency scanning (govulncheck + npm audit + Trivy)

**Workflow:** [`.github/workflows/vulncheck.yml`](../../.github/workflows/vulncheck.yml)
**Triggers:** every PR + push to `main`, plus nightly at 07:00 UTC.
**Fails the build on:** reachable Go vulnerabilities, npm audit
HIGH+, Trivy HIGH/CRITICAL on a built image.

Three parallel matrices:

### 2a. govulncheck (Go)

One job per workspace module (pkg + 12 services + 2 cmd/). Uses
Go's official `govulncheck` — this understands **reachability**, so
we don't gate on CVEs in unused code paths. Threshold in the
workflow env: `GOVULNCHECK_MIN_CVSS=7.0`.

### 2b. npm audit (web)

`npm audit --omit=dev --audit-level=high` against `web/package-lock.json`.
Dev-only deps don't ship to users; scanning them adds noise.

### 2c. Trivy (container images)

Builds a subset of service images (auth / document / policy / workflow
/ signature — the "privilege hot path" services) and scans the final
layer for HIGH/CRITICAL OS-package + library CVEs. Nightly run expands
to the full set of 15 services.

### When it fires

**Go CVE:**
1. Check the govulncheck report in the job log — it names the
   vulnerable symbol AND the call path from your code.
2. Upgrade the dep (`go get -u <pkg>@<fixed>`) and `go mod tidy` in
   the affected module.
3. If no fix is available, add a pragma comment above the import and
   document on the tech-debt ledger.

**npm HIGH:**
1. `cd web && npm audit --omit=dev` locally.
2. `npm audit fix` for patch-level fixes.
3. For breaking fixes: bump the dep in `web/package.json`, rebuild,
   run `npm test` + `npx playwright test` before merging.

**Trivy HIGH/CRITICAL:**
1. Usually a base-image update (`golang:1.26`, `alpine:3.x`).
2. Check `build/Dockerfile.go-service` — bump the base image, rebuild,
   re-run Trivy locally (`trivy image vaultdms-<svc>:scan`).

## Gate 3 — DAST (OWASP ZAP)

**Workflow:** [`.github/workflows/dast-zap.yml`](../../.github/workflows/dast-zap.yml)
**Triggers:** nightly at 03:00 UTC, or manual via `workflow_dispatch`.
**Fails the build on:** ZAP alert IDs marked `FAIL` in
[`ops/security/zap-baseline.conf`](../../ops/security/zap-baseline.conf).

Currently `FAIL`-flagged rule IDs: 40009 (SQLi), 40012–40018 (XSS
variants), 90019 (server-side code injection), 90020 (OS command
injection), 90022 (app error disclosure), 90033 (loosely scoped
cookie).

Passive-only — the baseline scan does **not** attempt authenticated
writes or fuzzing. An authenticated-mode DAST wave is tracked
separately; it needs a sandbox tenant + seeded credentials.

### When it fires

P0/P1 (HIGH/CRITICAL) ZAP findings **block the next release tag**
per [`docs/runbooks/wave-15-release-v1.3.0.md`](../runbooks/wave-15-release-v1.3.0.md).
Remediation path:

1. Download the HTML report from the run's artifacts
   (`zap-baseline-report`).
2. Reproduce locally — ZAP has a GUI (`zaproxy/zaproxy` Docker
   image) for interactive exploration.
3. Fix the underlying issue. If you're sure it's a false positive:
   - Add an `IGNORE` line to `ops/security/zap-baseline.conf`.
   - Add a dated + signed justification to `ops/security/zap-ignore.conf`.
   - Re-run the workflow with `workflow_dispatch`.

## Gate 4 — Secret scanning (gitleaks)

**Workflow:** [`.github/workflows/secret-scan.yml`](../../.github/workflows/secret-scan.yml)
**Pre-commit hook:** [`.pre-commit-config.yaml`](../../.pre-commit-config.yaml) (gitleaks protect)
**Config:** [`.gitleaks.toml`](../../.gitleaks.toml)
**Triggers:** every PR + push to `main`; pre-commit at every `git commit`.
**Fails the build on:** any detected secret.

Two passes:

1. **Local (pre-commit):** `gitleaks protect --staged`. Fast; scans
   only the staged diff. Runs before the commit is written.
2. **CI (full history):** `gitleaks detect` with `fetch-depth: 0`.
   Catches secrets pushed after a `git commit --no-verify`.

Allow-listed patterns live in `.gitleaks.toml`. Every addition to
the allow-list needs justification in a PR comment.

### When it fires

1. **Rotate the leaked credential immediately.** Even if the leak was
   in a feature branch, assume it's exposed.
2. Amend the commit (if the push hasn't happened) or BFG-rewrite the
   offending commit + force-push (if pre-push is still on the laptop).
   If the push already landed on `origin`, rotate AND open a
   security incident ticket.
3. Only after rotation: suppress the rule or adjust the pattern to
   avoid false positives.

## Escalation

| Gate        | First responder              | Escalate to              |
|-------------|------------------------------|--------------------------|
| SAST        | PR author                    | Platform-security on-call|
| Dep scan    | Module owner                 | Platform-security on-call|
| DAST        | Platform-security on-call    | Security lead            |
| Secret scan | PR author (rotate first)     | Security lead + IR chan  |

Security lead today: see `CODEOWNERS`. Incident channel:
`#vaultdms-security-ir` (create the ticket before pinging the channel).
