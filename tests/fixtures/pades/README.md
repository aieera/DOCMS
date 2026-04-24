# PAdES-B-LT fixture corpus

This directory holds signed PDFs the
`services/signature/internal/pades` validator runs against when
invoked with `-tags pades_corpus` (or `make test-pades`). CI does
NOT ship fixtures here — producing one requires the signer sidecar,
a signing certificate, and a TSA. Operators populate this directory
once per release as part of the Wave 9 / Wave 15.4 validation
cadence.

## What belongs here

- `*.pdf` files that are **valid PAdES-B-LT** — real signatures,
  real DSS dictionary, real DocTimeStamp. The Go validator asserts
  structural conformance on each file.
- Naming: `<variant>.pdf` where variant names the envelope shape,
  e.g. `single-signer.pdf`, `two-signers-sequential.pdf`,
  `countersignature.pdf`, `saved-signature-drawing.pdf`
  (Wave 15.4 — uses a saved profile image).

## What does NOT belong here

- Invalid / tampered PDFs (use the unit tests in `validator_test.go`
  for negative cases — synthetic payloads are enough).
- Raw DER certs, TSA replies, or private keys.
- Multi-hundred-MB stress corpora — cap each file at ~2 MB so `git
  clone` stays snappy. Large-scale corpora live off-repo behind the
  signer sidecar's own fixtures.

## Producing fixtures

See `docs/runbooks/15-pades-harness.md` for the end-to-end recipe:

1. Bring up the DSS sidecar (`docker compose --profile signer up`).
2. Run `dms-admin signatures produce-fixture --template single-signer
   --out tests/fixtures/pades/single-signer.pdf`.
3. Run `make test-pades` to confirm the validator accepts it.
4. Open the same PDF in Adobe Reader and record the green-tick
   LTV status in `docs/reports/pades-validation-<date>.md`.

## Current state

Empty. The corpus harness skips gracefully when no fixtures are
present (`t.Skipf` in `corpus_integration_test.go`) — that's the
intentional behaviour until the sidecar ships. Do NOT replace the
skip with a hard fail; the sidecar being absent is expected during
the Wave 15.x window.
