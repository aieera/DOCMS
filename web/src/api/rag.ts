import { api } from './client'

export interface RAGCitation {
  doc_id: string
  workspace_id: string | null
  page: number | null
  chunk_id: number | null
  section_path: string | null
  snippet: string
  score: number
}

export interface RAGQueryResponse {
  query_id: string
  answer: string
  citations: RAGCitation[]
  model: string
  input_tokens: number
  output_tokens: number
  cost_usd: number
  elapsed_ms: number
}

export type RAGFeedback = 'up' | 'down' | 'flag'

export async function queryRAG(input: {
  question: string
  workspaceId?: string
  model?: string
}) {
  const { data } = await api.post<RAGQueryResponse>('/intelligence/rag/query', {
    question: input.question,
    workspace_id: input.workspaceId,
    model: input.model,
  })
  return data
}

export async function sendRAGFeedback(
  queryId: string,
  feedback: RAGFeedback,
  note?: string,
) {
  await api.post(`/intelligence/rag/query/${queryId}/feedback`, {
    feedback,
    note,
  })
}
