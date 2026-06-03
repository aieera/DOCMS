# Remediation 19h — Wave 12.8: Vault + AWS KMS production adapters

**Date:** 2026-04-18
**Wave:** 12.8 · closes the ADR 0022 + ADR 0025 + Wave 6.1 stub items
that promised "prod swaps in Vault / AWS KMS."

## Recon

`pkg/crypto/kms.go` held two stubs since Phase B:

```go
type VaultKeyManager struct{}
func NewVaultKeyManager() (*VaultKeyManager, error) {
    return nil, fmt.Errorf("VaultKeyManager: not implemented until Phase 6")
}

type AWSKMSKeyManager struct{}
func NewAWSKMSKeyManager() (*AWSKMSKeyManager, error) {
    return nil, fmt.Errorf("AWSKMSKeyManager: not implemented until Phase 6")
}
```

`LocalKeyManager` (dev/on-prem) had been the only working
implementation — master secret in process memory. Prod needed
real adapters.

## What shipped

### VaultKeyManager

[pkg/crypto/kms.go](../../../pkg/crypto/kms.go):

- `VaultKeyManager{client, KeyPrefix}` wraps a narrow
  `VaultTransitClient` interface (GenerateDataKey / Decrypt /
  Rotate). The interface is the injection boundary — production
  wiring uses `github.com/hashicorp/vault/api` via a small
  adapter; tests stub directly.
- `KeyPrefix` prepends to every kekID so a shared Vault cluster
  can host multiple DMS deployments (e.g. `dms-staging/` +
  `dms-prod/`).
- `RotateKey` delegates to Transit's versioned rotation — old
  key versions keep decrypting pre-rotation ciphertext without
  callers doing anything.

### AWSKMSKeyManager

- `AWSKMSKeyManager{client, AliasPrefix}` wraps an `AWSKMSClient`
  interface (GenerateDataKey / Decrypt / ScheduleKeyDeletion).
  Production wiring uses `aws-sdk-go-v2/service/kms`.
- `keyID(kekID)` normalises `/` and `@` to `-` because AWS KMS
  alias names permit alphanumeric + `[_-]` only. A kekID like
  `vaultdms/tenant/<uuid>/eu-west-1@v2` maps to alias
  `alias/vdms-vaultdms-tenant-<uuid>-eu-west-1-v2`.
- `RotateKey` returns a typed error: AWS CMKs rotate via IaC
  alias provisioning, not at runtime. Surfacing the error
  prevents operators accidentally dropping into a retry loop.
- `ScheduleKeyDeletion` on the client interface hooks into the
  Wave 12.7 control-plane endpoint — the reaper (Wave 12.7b)
  calls this for tenants past their grace window.

### Errors

- `ErrKMSNotConfigured` is a typed sentinel returned when any
  adapter is constructed without a client (defensive; normal
  construction enforces via `NewX(client, ...)` nil-check).

## DoD

| Requirement | Status |
|---|---|
| Vault Transit adapter implementing KeyManager | ✅ |
| AWS KMS adapter implementing KeyManager | ✅ |
| Alias / key-prefix configurable | ✅ both adapters |
| Rotation semantics reflect backend reality (Vault versions, AWS IaC) | ✅ |
| Normalises kekID → backend-safe name | ✅ AWS slash-to-dash |
| Testable without real Vault / AWS | ✅ narrow interfaces + stubs |

## Tests

[pkg/crypto/kms_adapters_test.go](../../../pkg/crypto/kms_adapters_test.go)
— 5 unit tests:

1. `VaultKeyManager_PrefixesKekID` — KeyPrefix concatenates on
   GenerateDataKey, Decrypt, Rotate.
2. `VaultKeyManager_NilClientRejected` — constructor nil-check.
3. `AWSKMSKeyManager_NormalisesSlashesToDashes` — regional +
   rotation kekID maps to a valid alias.
4. `AWSKMSKeyManager_DefaultAliasPrefix` — empty prefix → `alias/vaultdms-`.
5. `AWSKMSKeyManager_RotateErrors` — RotateKey is a typed error
   by design (CMK rotation is IaC-only).

```
$ go test ./pkg/crypto/...
ok  github.com/aieera/sedoc/pkg/crypto  1.063s
```

## Deferred (logged in out-of-scope.md)

- **Real Vault API wrapper** — the production `VaultTransitClient`
  implementation that uses `hashicorp/vault/api`. The adapter
  works today with any narrow client; production wiring adds
  ~50 lines. Wave 12.8b.
- **Real AWS SDK wrapper** — same story, ~60 lines using
  `aws-sdk-go-v2`. Wave 12.8b.
- **Wire `kms_provider` config → adapter selection** in every
  service's `main.go`. Today each service constructs
  `LocalKeyManager` unconditionally; the factory that picks
  `local` vs `vault` vs `aws` based on `cfg.KMSProvider` lives
  with the SDK wrappers. Wave 12.8b.
- **Per-region AWS KMS CMKs** — ADR 0026 promised region-scoped
  masters; AWS KMS handles this natively via multi-region keys.
  The adapter supports it today (the `/<region>` suffix flows
  through), but operators need a documented IaC pattern for
  provisioning per-region CMKs. Runbook update, Wave 12.8b.

## Wave 12 scorecard

| Item | Status |
|---|---|
| 12.1 SMTP | ✅ |
| 12.2 Storage re-encrypt | ✅ |
| 12.3 Re-wrap CLI | ✅ |
| 12.4 Cross-service subject purge | ✅ |
| 12.5 Redaction fan-out | ✅ |
| 12.6 Connector OAuth purge | ✅ |
| 12.7 Control-plane admin endpoints | ✅ |
| **12.8 Vault / AWS KMS adapters** | ✅ this doc |
| 12.9 DSS Java sidecar | pending |

## Next prompt

**12.9 — DSS Java sidecar.** The last Wave 12 item; also the
Wave 9.2b follow-up. Ship a Gradle project under
`services/signature-signer/` that runs DSS 5.12.x and exposes
the `Sign` / `Verify` RPCs the Go `DSSSidecarSigner` shell
(Wave 9.2) calls.
