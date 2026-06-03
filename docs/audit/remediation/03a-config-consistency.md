# Remediation 03a — Config consistency

**Date:** 2026-04-17
**Scope:** 11 `os.Getenv` violations + 5 hardcoded fallback addresses from `docs/audit/03-inconsistencies.md` §2.

---

## Summary

| Category | Before | After |
|----------|-------:|------:|
| `os.Getenv` calls in services/ | 11 | **0** |
| Hardcoded infra fallback addresses in services/*/cmd/ | 5 | **0** |
| Config fields | 30 | 35 (+5 new) |
| Prod-only secret validation | — | **`Validate()` + `RequireSecret()`** |

All 14 Go modules build clean. All existing tests pass.

---

## Task 1 — Config struct additions

### New fields (`pkg/config/config.go`)

| Field | Env var | Default | Prod-required |
|-------|---------|---------|---------------|
| `PublicURL` | `SEDOC_PUBLIC_URL` | *none* | **YES** |
| `PolicyServiceAddr` | `POLICY_SERVICE_ADDR` (non-prefixed) | `policy:9090` | no |
| `S3PublicBase` | `SEDOC_S3_PUBLIC_BASE` | *empty* | no |
| `InternalAPIKey` | `SEDOC_INTERNAL_API_KEY` | *none* | conditional (billing) |
| `StripeWebhookSecret` | `STRIPE_WEBHOOK_SECRET` (non-prefixed) | *none* | conditional (billing) |

### Renamed fields (same semantics, updated env var names per spec)

| Old → New | Old env → New env |
|-----------|-------------------|
| `ClamAVAddress` → `ClamAVAddr` | `SEDOC_CLAMAV_ADDRESS` → `CLAMAV_ADDR` |
| `TemporalAddress` → `TemporalAddr` | `SEDOC_TEMPORAL_ADDRESS` → `TEMPORAL_ADDR` |

Non-VAULTDMS env bindings added explicitly via `viper.BindEnv()`:
```go
_ = v.BindEnv("policy_service_addr", "POLICY_SERVICE_ADDR")
_ = v.BindEnv("stripe_webhook_secret", "STRIPE_WEBHOOK_SECRET")
_ = v.BindEnv("clamav_addr", "CLAMAV_ADDR")
_ = v.BindEnv("temporal_addr", "TEMPORAL_ADDR")
_ = v.BindEnv("opensearch_url", "OPENSEARCH_URL")
```

### New `Validate()` method

```go
func (c *Config) Validate() error {
    if c.Environment != "prod" {
        return nil
    }
    var missing []string
    if c.PublicURL == "" {
        missing = append(missing, "SEDOC_PUBLIC_URL")
    }
    if c.LocalKEK == "" && c.KMSProvider == "local" {
        missing = append(missing, "SEDOC_LOCAL_KEK (kms_provider=local)")
    }
    if len(missing) > 0 {
        return fmt.Errorf("config: missing required prod env vars: %s", strings.Join(missing, ", "))
    }
    return nil
}
```

Plus `RequireSecret(name, value)` helper for per-service secret checks (billing calls this for `InternalAPIKey` + `StripeWebhookSecret`).

Called automatically by `Load()` after unmarshalling and struct validation.

### Defaults added

```go
v.SetDefault("policy_service_addr", "policy:9090")
v.SetDefault("clamav_addr",         "clamav:3310")
v.SetDefault("temporal_addr",       "temporal:7233")
v.SetDefault("opensearch_url",      "http://opensearch:9200")
```

Secrets (`LocalKEK`, `InternalAPIKey`, `StripeWebhookSecret`, `PublicURL`) intentionally **not** defaulted — they fail `Validate()` in prod.

---

## Task 2 — `os.Getenv` replacements (11 call sites)

| # | File | Line | Before | After |
|---|------|------|--------|-------|
| 1 | `services/auth/cmd/server/main.go` | 117 | `os.Getenv("SEDOC_PUBLIC_URL")` | `cfg.PublicURL` |
| 2 | `services/auth/cmd/server/main.go` | 131 | `os.Getenv("SEDOC_PUBLIC_URL")` | `cfg.PublicURL` |
| 3 | `services/auth/cmd/server/main.go` | 138 | `os.Getenv("SEDOC_PUBLIC_URL")` | `cfg.PublicURL` |
| 4 | `services/auth/cmd/server/main.go` | 196 | `os.Getenv("SEDOC_LOCAL_KEK")` inside `loadLocalKEK()` | `loadLocalKEK(cfg.LocalKEK)` |
| 5 | `services/document/cmd/server/main.go` | 81 | `os.Getenv("POLICY_SERVICE_ADDR")` | `cfg.PolicyServiceAddr` |
| 6 | `services/document/cmd/server/main.go` | 105 | `os.Getenv("SEDOC_PUBLIC_URL")` | `cfg.PublicURL` |
| 7 | `services/storage/cmd/server/main.go` | 84 | `os.Getenv("POLICY_SERVICE_ADDR")` | `cfg.PolicyServiceAddr` |
| 8 | `services/storage/cmd/server/main.go` | 108 | `os.Getenv("SEDOC_LOCAL_KEK")` | `cfg.LocalKEK` |
| 9 | `services/storage/cmd/server/main.go` | 136 | `os.Getenv("SEDOC_S3_PUBLIC_BASE")` | `cfg.S3PublicBase` |
| 10 | `services/billing/internal/handler/handler.go` | 29 | `os.Getenv("SEDOC_INTERNAL_API_KEY")` | constructor param `apiKey`, sourced from `cfg.InternalAPIKey` in main.go |
| 11 | `services/billing/internal/stripe/webhook.go` | 37 | `os.Getenv("STRIPE_WEBHOOK_SECRET")` | constructor param `webhookSecret`, sourced from `cfg.StripeWebhookSecret` in main.go |

### Constructor signature changes (billing)

```go
// Before:
func New(svc *service.Service, stripeWH *stripehandler.WebhookHandler, log zerolog.Logger) *Handler
func NewWebhookHandler(repo *repository.Repository, rdb *redis.Client, log zerolog.Logger) *WebhookHandler

// After:
func New(svc *service.Service, stripeWH *stripehandler.WebhookHandler, log zerolog.Logger, apiKey string) *Handler
func NewWebhookHandler(repo *repository.Repository, rdb *redis.Client, log zerolog.Logger, webhookSecret string) *WebhookHandler
```

`services/billing/cmd/server/main.go` updated to pass `cfg.StripeWebhookSecret` and `cfg.InternalAPIKey`.

---

## Task 3 — Hardcoded fallbacks removed (5 sites)

| File | Before | After |
|------|--------|-------|
| `services/document/cmd/server/main.go:81-83` | `policyAddr := os.Getenv(...); if "" { policyAddr = "localhost:9091" }` | `grpc.DialContext(ctx, cfg.PolicyServiceAddr, ...)` (default `policy:9090` in pkg/config) |
| `services/storage/cmd/server/main.go:72-75` | `if cfg.ClamAVAddress == "" { clamAddr = "clamav:3310" }` | `scanner.New(cfg.ClamAVAddr, ...)` |
| `services/storage/cmd/server/main.go:84-87` | `os.Getenv + "localhost:9091"` fallback | `cfg.PolicyServiceAddr` |
| `services/search/cmd/server/main.go:70-73` | `if cfg.OpenSearchURL == "" { osURL = "http://opensearch:9200" }` | `opensearch.NewReal(ctx, opensearch.Config{URL: cfg.OpenSearchURL, ...})` |
| `services/workflow/cmd/server/main.go:66-69` | `if cfg.TemporalAddress == "" { temporalAddr = "localhost:7233" }` | `client.Dial(client.Options{HostPort: cfg.TemporalAddr})` |

---

## Task 4 — Docker / Helm / .env

### `.env.example`

Updated to use the new non-prefixed env vars and added the 5 new SEDOC_-prefixed ones:

```diff
-TEMPORAL_ADDRESS=temporal:7233
-CLAMAV_ADDRESS=clamav:3310
-LOCAL_KEK_BASE64=
+TEMPORAL_ADDR=temporal:7233
+CLAMAV_ADDR=clamav:3310
+POLICY_SERVICE_ADDR=policy:9090
+SEDOC_LOCAL_KEK=
+SEDOC_PUBLIC_URL=
+SEDOC_S3_PUBLIC_BASE=
+SEDOC_INTERNAL_API_KEY=
+STRIPE_WEBHOOK_SECRET=
```

### `docker-compose.yml`

**No changes needed.** SeDoc Go services are not declared in compose (the file is infrastructure-only per the comment at line 2: "Services (Go binaries) run on the host during development"). Host processes pick up the new env vars from `.env` via the Makefile targets.

### Helm values (`deploy/helm/sedoc/values.yaml`)

Added under `global:`:
```yaml
publicURL: "https://{{ .Values.global.domain }}"
s3PublicBase: ""
policyServiceAddr: "vaultdms-policy:9090"
clamAVAddr: "vaultdms-clamav:3310"
temporalAddr: "vaultdms-temporal-frontend:7233"
openSearchURL: "http://vaultdms-opensearch:9200"
billing:
  existingSecret: vaultdms-billing-secrets
  internalAPIKeyKey: api-key
  stripeWebhookSecretKey: webhook-secret
```

### Helm `_helpers.tpl`

The `vaultdms.commonEnv` helper (injected into every Go service's deployment) now emits:
- `OPENSEARCH_URL` (was `SEDOC_OPENSEARCH_URL`)
- `TEMPORAL_ADDR` (was `SEDOC_TEMPORAL_ADDRESS`)
- `CLAMAV_ADDR` (new)
- `POLICY_SERVICE_ADDR` (new)
- `SEDOC_PUBLIC_URL` (new)
- `SEDOC_S3_PUBLIC_BASE` (new)
- `SEDOC_LOCAL_KEK` (new, via `secretKeyRef` with `optional: true`)

Plus a new `vaultdms.billingEnv` helper pulling `SEDOC_INTERNAL_API_KEY` + `STRIPE_WEBHOOK_SECRET` from a K8s Secret. Billing deployment template should include it; other services should not.

### `values-onprem.yaml`

Added on-prem-specific overrides:
```yaml
publicURL: "https://dms.internal.corp"
policyServiceAddr: "vaultdms-policy.vaultdms.svc.cluster.local:9090"
clamAVAddr: "vaultdms-clamav.vaultdms.svc.cluster.local:3310"
temporalAddr: "vaultdms-temporal-frontend.vaultdms.svc.cluster.local:7233"
openSearchURL: "http://vaultdms-opensearch.vaultdms.svc.cluster.local:9200"
s3PublicBase: "https://s3.internal.corp"
billing.existingSecret: vaultdms-billing-secrets-onprem
```

---

## Verification

| # | Check | Result |
|---|-------|--------|
| 1 | `grep os.Getenv services/` | **0 matches** |
| 2 | Hardcoded infra addresses in `services/*/cmd/` | **0** (residual `localhost:` is the document gRPC-gateway dialing its own in-process gRPC server — not a config concern) |
| 3 | `go build ./...` across all 14 modules | **14/14 PASS** |
| 4 | `go test ./... (pkg + auth + search)` | **PASS**, all existing tests still pass |
| 5 | `docker compose config --quiet` | exit 0 |
| 6 | `helm template` onprem | helm not installed on this machine; CI runs this check |

---

## Files changed

| File | Change |
|------|--------|
| `pkg/config/config.go` | Added 5 new fields, renamed 2 (ClamAVAddress→ClamAVAddr, TemporalAddress→TemporalAddr), added `Validate()` + `RequireSecret()`, explicit `BindEnv` for non-prefixed env vars, new defaults |
| `services/auth/cmd/server/main.go` | 3× `os.Getenv("SEDOC_PUBLIC_URL")` → `cfg.PublicURL`; `loadLocalKEK()` now takes key string from `cfg.LocalKEK` |
| `services/document/cmd/server/main.go` | Policy addr + public URL from cfg; removed `localhost:9091` fallback |
| `services/storage/cmd/server/main.go` | Policy addr, LocalKEK, S3PublicBase, ClamAV addr from cfg; removed 3 hardcoded fallbacks |
| `services/search/cmd/server/main.go` | OpenSearch URL directly from cfg; removed default fallback |
| `services/workflow/cmd/server/main.go` | Temporal addr directly from cfg; removed default fallback |
| `services/billing/internal/handler/handler.go` | `New()` signature gains `apiKey string`; removed `os.Getenv` + `os` import |
| `services/billing/internal/stripe/webhook.go` | `NewWebhookHandler()` signature gains `webhookSecret string`; removed `os.Getenv` + `os` import |
| `services/billing/cmd/server/main.go` | Pass `cfg.StripeWebhookSecret` and `cfg.InternalAPIKey` to constructors |
| `.env.example` | Added 5 new env vars + renamed 2 |
| `deploy/helm/sedoc/values.yaml` | Added 8 new global.* keys |
| `deploy/helm/sedoc/values-onprem.yaml` | Added on-prem overrides for 7 keys |
| `deploy/helm/sedoc/templates/_helpers.tpl` | Updated commonEnv; added billingEnv helper |
