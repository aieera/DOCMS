# ADR 0064 — LiteLLM tenant routing, fallback, circuit breaker

Date: 2026-05-06
Status: Accepted
Supersedes: nothing (was scheduled as ADR 0055 in the §6.9 blueprint;
0055 is taken by Doc Q&A. Same numbering shift as 0061/0062/0063.)

## Context

`services/intelligence/app/llm_gateway.py` already wraps litellm and
runs every workspace RAG, Doc Q&A, NER LLM tier, summarization, and
translation call through it. What it does today:

- loads per-tenant config from a Redis key written by *something*
  (no admin API to set it; tenants are stuck on the service default),
- retries once on `litellm.RateLimitError`, then surfaces the error,
- meters tokens + cost to a Redis hash that
  `/api/v1/admin/llm-usage` aggregates.

§6.9 of the blueprint calls out four things that are missing:

1. A first-class API + admin UI for setting the per-tenant
   provider/model/key.
2. **Fallback model** so a tenant pinned to OpenAI can degrade to
   Anthropic (or vice versa) on rate-limit / timeout, instead of
   surfacing the error to the user mid-Q&A.
3. **Circuit breaker** per (tenant, provider) so a misconfigured
   key or a sustained provider outage doesn't keep retrying for
   every call across the fleet.
4. **Air-gapped mode** that fails closed for any non-`vllm_local`
   call — the on-prem and FedRAMP-aspiring deploys cannot let the
   gateway accidentally egress to OpenAI.

This ADR captures the design we settled on. The implementation is
PR #43 and lives on `feat/llm-routing`.

## Decision

### Schema

`tenant_llm_config` (one row per tenant) holds:

| Column | Purpose |
|--------|---------|
| `provider` | Enum (`openai`, `anthropic`, `bedrock`, `vllm_local`, `custom`) drives which auth shape is used and whether air-gapped allows the call. |
| `model` | Primary model id in litellm format (e.g. `anthropic/claude-haiku-4-5`). Empty string falls back to the service default. |
| `fallback_model` | Used on rate-limit / timeout / breaker-open; empty disables fallback. |
| `api_key_encrypted` | AES-256-GCM ciphertext (same envelope as `ner_config.llm_api_key_encrypted`). Never returned in plaintext after write. |
| `api_key_set_at` | The admin UI shows "key set 5 days ago" without reading the ciphertext. |
| `base_url` | Optional override for self-hosted endpoints (vLLM, Azure, gateways). |
| `rate_limit_rpm` | Sliding-window per-minute cap; 0 disables. |
| `daily_budget_usd` | Soft cost cap; breach emits an event and the gateway short-circuits. 0 disables. |
| `air_gapped` | Forces `vllm_local`-only routing for this tenant regardless of the deploy-time `VAULTDMS_AIR_GAPPED` flag. |

`llm_circuit_breaker_state` (one row per (tenant, provider)) records
breaker state across replicas. The gateway also keeps an in-process
cache for hot-path latency — DB row is the recovery source on
restart.

We deliberately did **not** reuse `ner_config.llm_api_key_encrypted`.
The NER LLM tier is a small subset of the LLM surface (entity
extraction prompts), and tenants frequently want to point it at a
cheaper / on-prem model than the workspace RAG/Q&A path. Two rows
is the cleaner long-term shape; a follow-up may unify them once
that migration is worth the churn.

### Provider routing

```
┌──────────────────────┐
│ caller (RAG, Q&A,    │
│ summarize, NER, /llm)│
└──────────┬───────────┘
           │ tenant_id, messages
           ▼
┌────────────────────────┐    breaker open?    ┌──────────────────┐
│ load tenant_llm_config │────────yes─────────▶│ try fallback_model│
└──────────┬─────────────┘                     └────────┬──────────┘
           │ closed                                     │
           ▼                                            ▼
   air_gapped & provider≠vllm_local?       fallback also breaker-open?
           │ yes                              │ yes
           ▼                                  ▼
    raise AirGappedError              raise AllProvidersDownError
           │ no                              │ no
           ▼                                  ▼
   litellm.completion(...)              litellm.completion(...)
           │                                  │
   on RateLimitError /                        ▼
   Timeout / breaker trip ──────────▶  same path, with fallback model
           │
           ▼
   meter usage + emit dms.billing.llm.usage.v1
```

### Circuit breaker

Per-(tenant, provider). Closed → Open after **5** consecutive failures
(rate-limit, timeout, 5xx). Open → Half-open after **60** seconds —
the next call probes the provider; success closes, failure re-opens
for another 60 s. Counters reset on success. State persists to the
DB row + cached in-process for hot-path latency.

### Air-gapped enforcement

Two layers:
- Deploy-time env `VAULTDMS_AIR_GAPPED=1` forces every tenant's
  effective `air_gapped=true` and is the on-prem default.
- Per-tenant `air_gapped=true` lets a single tenant in a
  multi-tenant deploy pin to local vLLM only.

The gateway raises `AirGappedError` *before* the litellm call when
`air_gapped` is true and the resolved provider isn't `vllm_local`.
That's the load-bearing test in `test_llm_routing.py` — it doesn't
just check the error, it asserts no outbound network call is made.

### Cost metering

We keep the existing Redis hash counter (drives the admin dashboard's
30-day rolling window). Additionally, every completion now publishes
`dms.billing.llm.usage.v1` to JetStream's `BILLING_EVENTS` stream:

```json
{
  "type": "dms.billing.llm.usage.v1",
  "tenant_id": "…",
  "user_id": "…",
  "model": "anthropic/claude-haiku-4-5",
  "provider": "anthropic",
  "input_tokens": 850,
  "output_tokens": 64,
  "cost_usd_cents": 12,
  "elapsed_ms": 420
}
```

The billing service consumes this and rolls it into the same
per-tenant invoice line that workspace seats and storage use.

### API surface

| Method | Path | Auth | Notes |
|--------|------|------|-------|
| POST | `/api/v1/intelligence/llm/completions` | tenant + user | First-class completions endpoint. Used by the admin "Test" button and any future external integration. |
| GET | `/api/v1/admin/tenant/llm-config` | owner\|admin | Returns provider/model/fallback/base_url/rate_limit/budget/air_gapped + `key_set: bool` + `key_set_at`. **Never** returns the key. |
| PUT | `/api/v1/admin/tenant/llm-config` | owner\|admin | Patch any field; `api_key` is write-only and gets envelope-encrypted before insert. |

## Consequences

- **Vendor lock-in risk** drops: a tenant on OpenAI can flip a row to
  Anthropic without redeploying.
- **Cost-overrun blast radius** drops: per-tenant `daily_budget_usd`
  short-circuits before LLM invocation.
- **On-prem story unblocked**: air-gapped tenants can now demonstrably
  prove no external egress at the per-call level.
- **Adds a hot-path DB read** on every completion. Mitigated by 30 s
  Redis cache; the breaker check is also cached.
- **Two API key columns** (`ner_config.llm_api_key_encrypted` and
  `tenant_llm_config.api_key_encrypted`) until the unification PR.
  Documented above.
