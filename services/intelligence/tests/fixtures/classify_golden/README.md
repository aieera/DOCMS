# Classify golden corpus

Fixtures for [test_classify_golden.py](../../test_classify_golden.py). Validates the
tier-2 ML classifier against a frozen set of expected outputs.

## What goes here

10 representative PDFs covering the production category mix, plus their
expected classifications in [golden_outputs.json](golden_outputs.json).

| Slot | Suggested category | Filename convention |
|------|--------------------|---------------------|
| 1    | invoice            | `01-invoice.pdf`    |
| 2    | invoice            | `02-invoice.pdf`    |
| 3    | contract           | `03-contract.pdf`   |
| 4    | contract           | `04-contract.pdf`   |
| 5    | resume             | `05-resume.pdf`     |
| 6    | receipt            | `06-receipt.pdf`    |
| 7    | nda                | `07-nda.pdf`        |
| 8    | report             | `08-report.pdf`     |
| 9    | letter             | `09-letter.pdf`     |
| 10   | other              | `10-other.pdf`      |

Two per high-volume category (invoice, contract) so a model regression
shows up as 2 failures, not 1 — easier to triage drift vs. a one-off
mis-classification.

## Sourcing constraints

- **Licensing**: PDFs must be redistributable under this repo's license.
  Use synthetic/generated docs, public-domain templates, or docs you
  hold the rights to. Do not commit anything from a real customer
  tenant.
- **Size**: keep each PDF < 200 KB. The harness is a CI smoke test,
  not a load test.
- **Determinism**: the tier-2 classifier is non-deterministic across
  model versions but deterministic within one. Regenerate the golden
  whenever you bump the model.

## Regenerating the golden file

After adding/replacing PDFs:

```bash
cd services/intelligence
python scripts/regenerate_classify_golden.py
```

This calls the real classifier on every PDF in this directory, writes
the result to `golden_outputs.json`, and prints a diff against the
prior version so model drift is visible in the PR.

## Tolerance

The test asserts the predicted `category` matches exactly and that
`confidence` is within ±5% of the golden value. Tighten or loosen via
`CLASSIFY_GOLDEN_TOLERANCE` in [test_classify_golden.py](../../test_classify_golden.py).
