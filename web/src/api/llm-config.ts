import { api } from './client'

export interface TenantLLMConfig {
  provider: 'openai' | 'anthropic' | 'bedrock' | 'vllm_local' | 'custom'
  model: string
  fallback_model: string
  base_url: string | null
  rate_limit_rpm: number
  daily_budget_usd: number
  air_gapped: boolean
  // Write-only invariant: GET never returns the key. The UI shows
  // "Key set <relative time>" off these two fields.
  key_set: boolean
  key_set_at: string | null
  updated_at: string | null
}

export type TenantLLMConfigPatch = Partial<
  Pick<TenantLLMConfig,
    'provider' | 'model' | 'fallback_model' | 'base_url' |
    'rate_limit_rpm' | 'daily_budget_usd' | 'air_gapped'>
> & {
  // Empty string = clear; undefined = preserve; any other string = encrypt+store.
  api_key?: string
}

export async function getTenantLLMConfig() {
  const { data } = await api.get<TenantLLMConfig>('/admin/tenant/llm-config')
  return data
}

export async function updateTenantLLMConfig(patch: TenantLLMConfigPatch) {
  const { data } = await api.put<TenantLLMConfig>('/admin/tenant/llm-config', patch)
  return data
}

export interface LLMTestResponse {
  content: string
  model: string
  provider: string
  input_tokens: number
  output_tokens: number
  cost_usd: number
  elapsed_ms: number
  fallback_used: boolean
}

export async function testLLMCompletion(prompt: string, model?: string) {
  const { data } = await api.post<LLMTestResponse>('/intelligence/llm/completions', {
    messages: [{ role: 'user', content: prompt }],
    model,
    max_tokens: 200,
  })
  return data
}
