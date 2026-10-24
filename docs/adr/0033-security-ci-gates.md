# ADR 0033 — Security CI gates

- **Status:** Accepted · 2026-04-24
- **Closes:** pre-GA security-gate checklist ("SAST + dep scan + DAST
  + secret scan must gate merges" from the Wave 14.4 threat model).
- **Related:** ADR 0031 (internal-auth plane — produces several of
  the custom Semgrep rules), ADR 0032 (NATS subject namespacing —
  its linter runs adjacent to these gates).

## Context

Wave 14.4 landed the STRIDE threat model and called out a concrete
pre-GA gap: the CI had `gosec` + `govulncheck` + an auto-config
Semgrep run but no **curated rule packs**, no **custom rules** for
the repo's invariants, no **DAST**, and no **gitleaks**-grade secret
scanning. Post-Wave-15 tech-debt closure (T-D-1..T-D-9) added several
"don't ever do this" invariants (trusted-proxy, auth-header discipline,
HTTP DefaultClient ban, subject shapes) that need enforcement beyond
code review.

The threat model also identified two gate failure modes we want to
avoid on the way in:

- **Over-strict noise** — blanket "any CVE blocks merge" produces a
  pile of false-positive suppressions under release pressure, which
  is how real issues get buried.
- **Single point of failure** — a single gate (e.g. "we run
  Semgrep") leaves a blind spot to anything Semgrep can't see.
  Four gates, each with a narrow scope, fail different classes of
  issues.

## Decision

Four gates, each with an explicit scope and threshold.

### Gate composition

| Gate        | Tool chain                                  | Threshold                           | Cadence              |
|-------------|---------------------------------------------|-------------------------------------|----------------------|
| SAST        | Semgrep + 5 rulepacks + `.semgrep/` custom  | any ERROR                           | Every PR + push      |
| Dep scan    | govulncheck, npm audit --omit=dev, Trivy    | CVSS ≥ 7.0, audit HIGH, Trivy HIGH  | Every PR + nightly   |
| DAST        | OWASP ZAP baseline                          | ZAP rule-ID FAIL list               | Nightly + on-demand  |
| Secret scan | gitleaks (CI + pre-commit)                  | any detected secret                 | Every PR + commit    |

Thresholds align deliberately:

- **CVSS 7.0 = npm "high" = Trivy "HIGH"**. Staff don't have to
  re-learn the scoring for each tool.
- **"Any detected secret"** is an absolute threshold for gitleaks —
  no severity ladder, because a leaked secret is always remediated
  by rotation, never by acceptance.
- **Semgrep ERROR ≠ Semgrep WARNING**. ERROR blocks; WARNING
  uploads to the Security tab for triage. Custom rules
  (`.semgrep/*`) default to ERROR for invariants that cost us
  production bugs (T-D-1, T-D-2, T-D-5); the SQL-hygiene rules
  ship as WARNING initially so the repo's existing SQL can migrate
  without a big-bang cleanup.

### Why custom rules live in `.semgrep/` (not golangci.yml)

The golangci `forbidigo` config already blocks `http.DefaultClient`
repo-wide — that rule exists in both places intentionally.
Duplication costs:

- The golangci rule is faster (go-only, cached), runs in `make lint`,
  catches dev-loop regressions before push.
- The Semgrep rule is cross-language (useful when a Python or Node
  service eventually needs the same invariant) AND gates PRs even
  when `make lint` is skipped.

## Consequences

### Positive

- Four independent gates raise the bar for "what can silently land
  on main" compared to the Wave 14.4 baseline.
- Custom Semgrep rules are source-controlled, grep-able, and easy to
  extend when a new ADR introduces an invariant.
- Pre-commit hook catches secrets before they're written to `.git/`,
  which is the only cheap remediation window — a secret that's
  already in history requires credential rotation regardless of
  how fast CI catches it.

### Negative / risk

- **Runtime cost**: four new jobs add ~5–10 minutes to the PR gate
  in the worst case. Semgrep + Trivy are the slowest; both have
  upload caching, but cold-cache runs are expensive. Split the
  vulncheck matrix (Go modules) into parallel legs so the wall
  clock stays under ~8 min.
- **Noise ratchet**: any new rule that's not already satisfied
  produces a one-time cleanup backlog. The mitigation is to
  introduce new rules in WARNING mode first, burn down the
  findings, then promote to ERROR.
- **DAST cadence**: nightly only. Won't catch a regression that
  lands and ships the same day. The release-tag gate
  (`docs/runbooks/wave-15-release-v1.3.0.md` P0/P1 rule) provides
  the final backstop before a tag publishes.

## Alternatives considered

1. **CodeQL** instead of Semgrep. CodeQL has deeper Go analysis, but
   (a) custom rules are a bigger learning curve (CodeQL QL language
   vs Semgrep YAML patterns), and (b) our repo is cross-language; a
   Go-centric tool would still need a separate Python/JS path.
2. **Snyk** instead of Trivy. Snyk has better reachability data but
   requires a paid tier for private repos. Trivy + govulncheck covers
   ~90% of the signal at zero license cost; revisit if the pilot tier
   becomes the constraint.
3. **Full authenticated DAST instead of passive baseline.** Out of
   scope for this ADR — authenticated DAST needs a sandbox tenant
   + write-safe test data. Passive baseline ships now; the
   authenticated wave is a separate proposal (tracked in
   `docs/backlog/out-of-scope.md`).
4. **Single consolidated workflow** vs one workflow per gate. Opted
   for one per gate so individual gates can be re-run / skipped /
   muted independently — faster iteration, clearer job names in the
   PR status checks.

## Rollout

1. Ship the four workflows + pre-commit config in one PR (this one).
2. Keep the existing `security-semgrep`, `security-go`, `security-npm`,
   `security-trivy` jobs in `ci.yml` for one release cycle as a
   belt-and-suspenders measure.
3. After the new workflows run clean for one full week on `main`,
   delete the duplicates from `ci.yml`. The `docs/security/ci-security-gates.md`
   operator doc becomes the canonical description.
4. First failing suppression — an allow-list add or ERROR → WARNING
   demotion — goes via PR review, **not** a quick-fix commit. The
   PR description must link to an open ticket / ADR amendment.
