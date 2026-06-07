-- Revert: drop 'gemini' from the allowed providers. Any rows still on
-- provider='gemini' must be migrated off first, or the new constraint
-- will fail to validate.
ALTER TABLE tenant_llm_config DROP CONSTRAINT tenant_llm_config_provider_check;
ALTER TABLE tenant_llm_config ADD CONSTRAINT tenant_llm_config_provider_check
    CHECK (provider IN ('openai', 'anthropic', 'bedrock', 'vllm_local', 'custom'));
