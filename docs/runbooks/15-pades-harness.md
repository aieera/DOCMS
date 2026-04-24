# Runbook — PAdES-B-LT validation harness

**Owner:** Signature Engineering · **Last rehearsed:** *(fill on next release)*

## What it does
Closes the Wave 9 / Wave 15.4 DoD line: *"resulting PDF validates as PAdES-B-LT in Adobe."* The harness is a **two-tier** validator:

1. **Go structural validator** (`services/signature/internal/pades/validator.go`). Runs on every PR via `make test-pades` (when fixtures exist). Catches missing `/DSS`, missing DocTimeStamp, or a `/ByteRange` that covers the whole file (tamper-window absence).
2. **External cryptographic validation** via EU DSS + Adobe Reader. Runs once per release; captures the green-tick LTV status that regulators look for.

The Go validator is there so the structural regressions don't slip through between quarterly crypto validations. Neither tier is complete on its own.

## Layout

- `services/signature/internal/pades/validator.go` — the validator.
- `services/signature/internal/pades/validator_test.go` — synthetic unit tests (always on).
- `services/signature/internal/pades/corpus_integration_test.go` — walks `tests/fixtures/pades/*.pdf` under `//go:build pades_corpus`.
- `tests/fixtures/pades/` — operator-managed corpus of real signed PDFs.
- `make test-pades` — convenience target for the corpus walker.

## Tier 1 — structural (in-repo)

### What runs automatically
The unit tests in `validator_test.go` always execute with `go test ./...`. They exercise synthetic PDF payloads and pin the validator's behaviour.

### What the operator runs before a release
```sh
# Produce a fresh corpus from the signer sidecar.
docker compose --profile signer up -d
dms-admin signatures produce-fixture --template single-signer --out tests/fixtures/pades/single-signer.pdf
dms-admin signatures produce-fixture --template two-signers-sequential --out tests/fixtures/pades/two-signers.pdf
dms-admin signatures produce-fixture --template saved-signature-drawing --out tests/fixtures/pades/saved-signature.pdf

# Run the validator.
make test-pades
```
Expected output: `ok services/signature/internal/pades` with one subtest per fixture. Any failure surfaces the missing structural element.

### What the validator guarantees
A PDF marked `IsValidPAdESBLT()` has:
- ≥ 1 `/Type /Sig` dictionary.
- ≥ 1 DocTimeStamp (`/SubFilter /ETSI.RFC3161`).
- A `/DSS` entry for offline validation material.
- A `/ByteRange` that leaves a tamper-detection window (total covered bytes < file length).

### What the validator does NOT guarantee
- Certificate-chain validity, OCSP/CRL freshness, TSA signature correctness. These are cryptographic properties that need the EU DSS library (Java) or the Adobe Reader validator. Tier 2 owns them.

## Tier 2 — cryptographic (external)

### Adobe Reader
Open each corpus file in Adobe Reader. Confirm:
1. Green tick in the Signatures pane.
2. "Signature is valid" header.
3. "Signature includes an embedded timestamp" from the signature properties panel.
4. "Long-term validation information was included" indicator.

Record the screenshot set in `docs/reports/pades-validation-<YYYY-MM-DD>.md`.

### EU DSS validator (automatable)
```sh
docker run --rm -v "$PWD/tests/fixtures/pades:/in" \
    ghcr.io/europa-eu/dss-validator:6.1 \
    validate --profile PAdES-BASELINE-LT --dir /in --format json > dss-report.json
jq '.validationReports[].conclusion.Indication' dss-report.json
```
Expected: every report's `Indication` is `TOTAL_PASSED`.

## Failure modes

| Symptom | Likely cause | Fix |
|---|---|---|
| Go validator says `HasDSS=false` | Signer produced a B-T profile instead of B-LT | Check sidecar config `pades.profile=B-LT`. |
| Go validator says `ByteRangeCoversWholeFile=true` | Sidecar bug or replay of an unsigned PDF | File a P0 — tamper-evidence is broken. |
| DSS says `INDETERMINATE` with `NO_POE` | Missing timestamp / DSS | Re-sign with TSA enabled, retry. |
| Adobe shows yellow warning, not green tick | Chain root not in Adobe's trust list (AATL) | Either bundle the root in the PDF's DSS or document that the customer must add it to their trusted list. |

## Cadence

- **Every PR**: the Go validator's unit tests run in the default `go test ./...`.
- **Every release tag**: `make test-pades` against a freshly-produced fixture corpus + Adobe Reader spot-check on at least `single-signer.pdf`.
- **Quarterly**: full EU DSS validation run recorded in `docs/reports/pades-validation-<date>.md`.

## Related

- ADR 0025 — PAdES library choice (DSS sidecar).
- `docs/runbooks/15-saved-signatures.md` — PAdES visual-appearance source (Wave 15.4); confirms saved-signature profiles do NOT change the cryptographic signature.
- `docs/backlog/out-of-scope.md` — Wave 9.2b entries for "Adobe Reader validation harness in CI", now partly addressed by Tier 1 of this runbook.
