-- Add 'gemini' (Google Gemini via litellm's gemini/ namespace) to the
-- set of LLM providers a tenant may configure. Mirrors the routes.py
-- validation tuple and the frontend PROVIDER_OPTIONS.
ALTER TABLE tenant_llm_config DROP CONSTRAINT tenant_llm_config_provider_check;
ALTER TABLE tenant_llm_config ADD CONSTRAINT tenant_llm_config_provider_check
    CHECK (provider IN ('openai', 'anthropic', 'bedrock', 'vllm_local', 'custom', 'gemini'));
