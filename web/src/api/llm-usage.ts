import { api } from './client'

export interface LLMUsageRow {
  model: string
  calls: number
  input_tokens: number
  output_tokens: number
  cost_usd: number
}

export interface LLMUsageResponse {
  tenant_id: string
  by_model: LLMUsageRow[]
  totals: {
    calls: number
    input_tokens: number
    output_tokens: number
    cost_usd: number
  }
}

export async function getLLMUsage() {
  const { data } = await api.get<LLMUsageResponse>('/admin/llm-usage')
  return data
}
