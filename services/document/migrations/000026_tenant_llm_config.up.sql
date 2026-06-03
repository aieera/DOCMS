-- ADR 0081 — per-tenant LiteLLM routing config.
--
-- Each tenant picks its own provider, model, fallback, and rate
-- limit. The intelligence service's llm_gateway loads this row on
-- every completion (cached briefly in Redis) and routes accordingly.
-- The previously-shipped ner_config.llm_api_key_encrypted stays
-- where it is — it powers a different code path (NER ensemble's
-- LLM tier) and tenants may want a different key for that surface.

CREATE TABLE tenant_llm_config (
    tenant_id              UUID         NOT NULL REFERENCES organizations(id),

    -- Provider determines (a) which litellm `model` namespace is
    -- legal (`openai/`, `anthropic/`, `bedrock/`, `vllm/…`), (b)
    -- which auth shape is used (`api_key` vs `aws_access_key_id`),
    -- and (c) whether the call is allowed in air-gapped mode (only
    -- `vllm_local` is). Free-text `custom` covers Azure OpenAI, Cohere,
    -- etc. without needing a schema bump.
    provider               TEXT         NOT NULL DEFAULT 'anthropic'
                                        CHECK (provider IN
                                            ('openai','anthropic','bedrock','vllm_local','custom')),

    -- Primary completion model (litellm-style id, e.g.
    -- `anthropic/claude-haiku-4-5`). Empty string means "use the
    -- service default" so a tenant can opt into routing without
    -- having to know the litellm id.
    model                  TEXT         NOT NULL DEFAULT '',

    -- Fallback model used when the primary trips its circuit breaker
    -- or returns a rate-limit / timeout error. Empty disables
    -- fallback (the call simply errors out — preferred for tenants
    -- with strict provider-pinning compliance asks).
    fallback_model         TEXT         NOT NULL DEFAULT '',

    -- AES-256-GCM ciphertext of the provider API key (or AWS access
    -- key for bedrock — see runbook for the JSON layout). Nonce-
    -- prepended, base64. Wire-compatible with services/document's
    -- ner_service.go::encryptTenantSecret + intelligence/secrets.py.
    api_key_encrypted      TEXT,
    -- Timestamp of the last write so the admin UI can show
    -- "key set 5 days ago" without ever reading the ciphertext back.
    api_key_set_at         TIMESTAMPTZ,

    -- Optional override for self-hosted endpoints (vLLM, Azure,
    -- proxies). Most tenants leave this NULL and let litellm
    -- pick the provider URL.
    base_url               TEXT,

    -- Per-tenant rate limit (requests per minute, sliding window
    -- enforced in Redis). 0 disables the gate and falls back to
    -- the global semaphore. The §6.9 spec calls this `rate_limit`.
    rate_limit_rpm         INTEGER      NOT NULL DEFAULT 60
                                        CHECK (rate_limit_rpm >= 0),

    -- Soft cap (USD) on a tenant's daily LLM cost. The billing
    -- aggregator emits an alert event when this is breached;
    -- enforcement happens in the gateway by short-circuiting to a
    -- `quota_exceeded` error. 0 disables.
    daily_budget_usd       NUMERIC(10,2) NOT NULL DEFAULT 0
                                         CHECK (daily_budget_usd >= 0),

    -- Air-gapped mode forces vllm_local routing only — the gateway
    -- rejects any call that would hit an external provider, even if
    -- model/api_key are set. Mirrors the deploy-time
    -- SEDOC_AIR_GAPPED env at per-tenant granularity for
    -- multi-tenant on-prem installs.
    air_gapped             BOOLEAN      NOT NULL DEFAULT FALSE,

    created_at             TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ  NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id)
);

ALTER TABLE tenant_llm_config ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_llm_config FORCE  ROW LEVEL SECURITY;
CREATE POLICY tenant_llm_config_tenant_isolation ON tenant_llm_config
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY tenant_llm_config_tenant_isolation_insert ON tenant_llm_config
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);


-- ---- llm_circuit_breaker_state ---------------------------------------
-- Lightweight key/value-style record of per-(tenant, provider) breaker
-- state so a multi-replica intelligence deployment shares the same
-- view of "this provider is currently open". The gateway also keeps an
-- in-process copy for hot-path latency; the row is the source of truth
-- when a replica restarts.
CREATE TABLE llm_circuit_breaker_state (
    tenant_id        UUID         NOT NULL REFERENCES organizations(id),
    provider         TEXT         NOT NULL,
    state            TEXT         NOT NULL DEFAULT 'closed'
                                  CHECK (state IN ('closed','open','half_open')),
    -- Rolling failure count in the current window.
    failure_count    INTEGER      NOT NULL DEFAULT 0,
    -- When state='open', the timestamp it should transition to
    -- half_open (gateway probes with a single request after this).
    open_until       TIMESTAMPTZ,
    -- Last error class for the runbook + admin UI ("rate_limit",
    -- "timeout", "auth_failed", etc.).
    last_error_kind  TEXT,
    updated_at       TIMESTAMPTZ  NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, provider)
);

ALTER TABLE llm_circuit_breaker_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE llm_circuit_breaker_state FORCE  ROW LEVEL SECURITY;
CREATE POLICY llm_breaker_tenant_isolation ON llm_circuit_breaker_state
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY llm_breaker_tenant_isolation_insert ON llm_circuit_breaker_state
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
