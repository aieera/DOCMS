# Runbook — PAdES-LTV Tier-2 Validation

Sister doc to [ADR 0072](../adr/0072-pades-ltv.md).

## Tier model

| Tier   | What it checks                                        | How                                                     |
|--------|--------------------------------------------------------|---------------------------------------------------------|
| Tier-1 | CMS verifies, byte-range covers the doc, chain walks  | `services/signature/internal/pades` — pure Go, runs in-process. No JVM, no network if `SkipNetworkLookups`. |
| Tier-2 | The output is acceptable to **independent** validators | EU Commission DSS demo + Adobe Reader. Catches drift in our /DSS embed shape. |

Tier-1 protects every Verify call. Tier-2 is the periodic
correctness check on Sign + Embed output — a real reader's
opinion on PDFs we just produced.

## EU Commission DSS validator (automated where possible)

The Commission hosts a public REST validator at:

```
https://ec.europa.eu/digital-building-blocks/DSS/webapp-demo/services/rest/validation/validateSignature
```

It accepts a JSON body with the PDF base64-encoded; returns an
ETSI Validation Report (XML or JSON depending on Accept header).

Test wrapper lives at
[services/signature/internal/pades/tier2_test.go](../../services/signature/internal/pades/tier2_test.go)
behind `//go:build tier2` so it stays out of the default test
run. Run it from the same nightly workflow that runs the eSign
sandbox tests:

```yaml
- name: Tier-2 — EU DSS validator
  run: go test -tags tier2 -count=1 -v ./services/signature/internal/pades/...
```

The harness POSTs a known-good signed PDF (from `testdata/`) and
asserts the resulting Validation Report has:

- `SignatureValidity = TOTAL_PASSED`
- `LTVSatus = ENABLED` (when the input is B-LT or above)

A PASS proves our /DSS shape + embed sequence are conformant. A
FAIL means the EU validator rejected something — usually our /VRI
key encoding or an ASN.1 quirk in OCSP responses we forwarded
verbatim.

> **Privacy**: the Commission's demo validator logs the document
> hash for ~7 days. Don't feed real customer PDFs through it; the
> harness uses synthetic test fixtures only.

## Adobe Reader (manual)

There's no Adobe CLI we can drive, so this is a once-per-release
manual procedure:

1. Generate a fresh B-LT signed PDF in dev:
   ```
   go run ./cmd/dev-signer -in testdata/sample.pdf -out /tmp/signed.pdf -level B-LT
   ```
2. Open `/tmp/signed.pdf` in Adobe Acrobat Reader (Windows or macOS,
   recent versions only — the "long-term validation" UX changed in
   Reader 2022.x).
3. **Expected**: green ribbon at top — *"Signed and all signatures
   are valid"* — and a sidebar item *"Long-term validation
   information has been added to this signature"*.
4. **Failure modes worth reporting**:
   - "Signature is valid, but signer's identity is unknown" → trust
     anchor not in Adobe's AATL. That's a configuration call (the
     dev cert isn't AATL-trusted; use a real QTSP-issued cert for
     the manual check or expect this state).
   - "Document has been modified since signing" → our incremental
     update broke ByteRange. **Bug**; file with priority.
   - "Long-term validation information could not be added" → /DSS
     shape is wrong. **Bug**; file with priority.

Document the result in the release-readiness checklist under
`PAdES-LTV (Tier-2)`. Two consecutive PASSes (current + previous
release) is the bar for tagging a release `ltv-stable`.

## What to do when Tier-2 fails

Tier-1 still gates Verify. If Tier-2 starts failing, signed PDFs
keep verifying inside SeDoc but won't satisfy external auditors.
Severity: SEV-3 (no customer outage) → SEV-2 if the broken output
already shipped to a customer.

1. Re-run the exact same input through both validators. If Adobe
   passes but EU DSS fails → likely our /VRI structure is too
   permissive; tighten parser.
2. If both fail → check git diff since the last green run;
   `services/signature/internal/pades/dss.go` is the usual
   suspect.
3. Roll back the `pades` package to the last green commit while
   the fix is in flight. The Verifier's Tier-1 pass keeps the
   in-app UX functional throughout.

## Prerequisites for a clean Tier-2 run

- Network egress from CI to `ec.europa.eu` (the Commission
  validator). If your runner is in a locked-down VPC, mirror the
  validator into your network — the Commission publishes the DSS
  webapp Docker image.
- A B-LT-grade signed PDF in `testdata/`. Regenerate by running
  the dev-signer against a stable test cert + a real qualified TSA
  (the test cert can be self-signed; the TSA token must come from
  a real QTSP, otherwise the EU validator's chain walk fails on
  the timestamp).
