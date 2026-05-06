# LLM provider operations

Runbook for the per-tenant LiteLLM routing introduced in ADR 0064.

## Tables

- `tenant_llm_config` — one row per tenant. Source of truth for
  provider, model, fallback, encrypted API key, rate limit, daily
  budget, air-gapped flag.
- `llm_circuit_breaker_state` — per-(tenant, provider) breaker
  recovery state. Hot-path read is the in-process cache; this row
  is for replica restart + admin visibility.

## KEK source — Vault Transit vs local AES-GCM

The intelligence service envelope-encrypts the `api_key_encrypted`
column with one of two backends, picked at runtime:

| When... | Backend | Ciphertext shape |
|---------|---------|------------------|
| `VAULT_ADDR` + `VAULT_TOKEN` + `VAULT_TRANSIT_KEY` all set | Vault Transit — KEK material lives **inside Vault**; every encrypt/decrypt round-trips to `/v1/transit/{op}/{key}` | `vault:v1:...` |
| `VAULTDMS_LOCAL_KEK` set, Vault not | In-process AES-256-GCM | base64(`nonce` + `ct` + `tag`) |
| neither | encrypt returns None → admin PUT 503s | n/a |

Decrypt detects which scheme produced a row by prefix and dispatches
back to the matching backend. **This is what makes the local→Vault
migration incremental** — existing rows written with the local KEK
keep decrypting via the in-process path after Vault is rolled out;
only new writes go through Vault.

### Enabling Vault Transit on an existing deploy

```sh
# 1. Configure the Transit secrets engine in Vault (one-time).
vault secrets enable transit
vault write -f transit/keys/vaultdms-tenant-secrets

# 2. Mint a token with encrypt + decrypt on that key only.
vault policy write vaultdms-llm-secrets - <<POL
path "transit/encrypt/vaultdms-tenant-secrets" { capabilities = ["update"] }
path "transit/decrypt/vaultdms-tenant-secrets" { capabilities = ["update"] }
POL
vault token create -policy=vaultdms-llm-secrets

# 3. Set the three env vars on every intelligence pod.
VAULT_ADDR=https://vault.internal:8200
VAULT_TOKEN=<token from step 2>
VAULT_TRANSIT_KEY=vaultdms-tenant-secrets

# 4. KEEP VAULTDMS_LOCAL_KEK set during the rollout window so the
#    decrypt fallback path can still read rows written before
#    Vault Transit was enabled. Drop it only after every existing
#    row has been re-encrypted (admin re-paste of each api_key, OR
#    a one-shot migration job — TODO).
```

The migration is intentionally lazy — there's no big-bang re-encrypt
step. Re-pasting an api_key in `/admin/tenant/ai` rewrites that row
under the new backend.

### Vault outage behavior

When Vault is configured but unreachable:
- **Encrypt**: falls back to local AES-GCM if `VAULTDMS_LOCAL_KEK` is
  also set. The new row's ciphertext lacks the `vault:` prefix and
  decrypts via the local path going forward (mixed-mode is fine).
  If neither backend is available the PUT 503s — admin sees the
  outage.
- **Decrypt**: existing `vault:v1:...` rows return None until Vault
  recovers. The LLM tier degrades to "no plaintext available" —
  callers (RAG, NER LLM, /llm/completions) silently fall through
  to the deploy-default key as if no per-tenant config was set.
  Logs warn-level `vault decrypt failed`.

## Routine ops

### Setting a tenant's API key

Admin UI: `/admin/tenant/ai`. The key is write-only — once saved, the
GET endpoint only ever returns `key_set: true` and `key_set_at`.
Plaintext leaves the browser exactly once, encrypted at rest with
the deploy's `VAULTDMS_LOCAL_KEK` (or Vault transit in cloud).

CLI fallback (when the UI is unavailable):

```sh
docker exec -e PGPASSWORD=devpassword vaultdms-postgres \
  psql -U vaultdms -d vaultdms <<SQL
SELECT set_config('app.current_tenant', '<tenant-id>', true);
INSERT INTO tenant_llm_config (tenant_id, provider, model, api_key_encrypted, api_key_set_at)
VALUES ('<tenant-id>', 'anthropic', 'anthropic/claude-haiku-4-5', '<base64-AESGCM>', now())
ON CONFLICT (tenant_id) DO UPDATE
   SET provider = EXCLUDED.provider,
       model    = EXCLUDED.model,
       api_key_encrypted = EXCLUDED.api_key_encrypted,
       api_key_set_at = now();
SQL
```

The ciphertext format is the same nonce-prepended AES-256-GCM blob
used by `ner_config.llm_api_key_encrypted` —
`services/intelligence/app/secrets.py::decrypt_tenant_secret` is the
canonical decoder; encryption helpers in `pkg/crypto/secrets.go`.

### Switching a tenant from OpenAI to Anthropic

```sh
PUT /api/v1/admin/tenant/llm-config
{
  "provider":      "anthropic",
  "model":         "anthropic/claude-sonnet-4-6",
  "fallback_model": "anthropic/claude-haiku-4-5",
  "api_key":       "sk-ant-…"
}
```

Set `fallback_model` empty to disable degradation; set it to the
old provider's model id to keep cross-provider failover.

### Air-gapped enforcement

- Deploy-time: set `VAULTDMS_AIR_GAPPED=1` in the intelligence
  service's env. Forces every tenant to `vllm_local` only — overrides
  per-tenant `air_gapped=false`.
- Per-tenant: PATCH `air_gapped: true` to pin a single tenant to
  vLLM in a shared deploy.

The smoke check that we're actually airgapped:

```sh
# Should 403 with provider=anthropic & air_gapped=true
curl -X POST $INTEL/api/v1/intelligence/llm/completions \
  -H "X-Tenant-ID: <air-gapped-tenant>" \
  -H "X-User-ID:   <user>" \
  -d '{"messages":[{"role":"user","content":"hello"}]}'
# {"detail":"air-gapped tenant cannot use external provider"}
```

## Incidents

### "Q&A is timing out for tenant X"

1. Hit `/admin/tenant/ai` for that tenant — read the breaker pill in
   the header. If it says **OPEN**, the gateway has cut over to
   `fallback_model`; `last_error_kind` tells you why
   (`rate_limit`/`timeout`/`auth_failed`).
2. If `auth_failed`: the API key was rotated provider-side. Walk the
   tenant admin through pasting the new key in the UI.
3. If `rate_limit`: bump `rate_limit_rpm` only after confirming the
   tenant actually has the provider-side quota; otherwise a higher
   RPM just exhausts faster.
4. If `timeout`: check the provider's status page first. If green,
   inspect `base_url` — a stale gateway can manifest as constant
   timeouts.

The breaker auto-recovers: 60 s after the last failure it transitions
half-open and probes with the next call. Force-close manually if
needed:

```sql
SELECT set_config('app.current_tenant', '<tenant-id>', true);
UPDATE llm_circuit_breaker_state
   SET state='closed', failure_count=0, open_until=NULL
 WHERE tenant_id='<tenant-id>' AND provider='<provider>';
```

### "Daily budget alert"

`daily_budget_usd` set on a tenant tripped. The aggregator emits
`dms.billing.llm.budget_exceeded.v1`; the gateway returns
HTTP 402 (`quota_exceeded`) until midnight UTC. Override mid-day by
PATCHing `daily_budget_usd` to a higher value.

### "All tenants are 5xx-ing"

That's a global litellm or upstream provider issue, not a per-tenant
config problem. Check the intelligence pod logs for
`litellm.exceptions.APIConnectionError` first; if it's pinned to one
provider, every tenant on that provider has flipped breaker-open
within 5 calls and is now using fallback. Confirm with:

```sql
SELECT provider, count(*)
  FROM llm_circuit_breaker_state
 WHERE state='open'
 GROUP BY provider
 ORDER BY 2 DESC;
```

## On-call cheat sheet

| Symptom | First check |
|---------|-------------|
| /ask returns 402 | `daily_budget_usd` for that tenant |
| /ask returns 403 "air-gapped" | `air_gapped` flag + provider |
| /ask returns "I don't know." | Empty allowed_doc_ids — RAG, not LLM |
| Token counts in admin show 0 | Streaming path didn't get usage; fall-through tier kicked in (see llm_gateway.py comment) |
| Cost dashboard frozen | Redis hash TTL'd at 30 days — expected for inactive tenants |
