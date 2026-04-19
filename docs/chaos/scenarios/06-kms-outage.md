# Chaos 06 — KMS outage

## Premise

Block every call to Vault / AWS KMS for 5 minutes. Encrypt
paths must fail-closed; decrypt paths must tolerate cached
DEKs where applicable.

## Setup

- Chaos-mesh CRD: `NetworkChaos` partition between every
  service pod and the KMS endpoint (Vault transit or AWS KMS
  VPC endpoint).
- k6 scenario 01-document-crud running uploads + downloads.

## Verification

### Expected behaviour

- **Uploads fail-closed:** `POST /storage/uploads/initiate`
  returns 503 with a `KMS_UNAVAILABLE` body. Queue depth on
  the client side accumulates; k6 retries after
  `Retry-After: 30`.
- **Downloads of fresh documents fail:** DEK unwrap needs KMS
  for any blob whose DEK isn't cached. Cache TTL per
  `pkg/crypto.LocalKeyManager` + production adapter settings.
- **Downloads of warm documents succeed** if the unwrapped DEK
  is already in the per-request cache from the last 5 minutes.

### Non-expected behaviour

- **Data loss:** a half-written ciphertext without its DEK
  persisted. The upload path must commit the DEK to
  `content_blobs` in the same tx as the blob metadata.
- **Crash loop:** services should degrade, not die. No `panic`
  on KMS timeout.

## Fail-the-test triggers

- Any successful upload during the outage window (means we
  weren't actually partitioned).
- Any download path leaking a 500 instead of a typed 503.
- Service crash / OOM / panic.
- After KMS recovery, any document stuck inaccessible (DEK
  row lost). Every upload started before the outage must be
  either fully committed or fully rolled back.
