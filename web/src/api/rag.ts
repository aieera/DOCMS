import { api } from './client'

export interface RAGCitation {
  doc_id: string
  document_title: string | null
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

export interface WorkspaceAISettings {
  rag_enabled: boolean
  answer_model: string
  embedding_model: string
  rag_queries_per_day: number
  updated_at: string | null
}

export async function getWorkspaceAISettings(workspaceId: string) {
  const { data } = await api.get<WorkspaceAISettings>(
    `/intelligence/workspaces/${workspaceId}/ai-settings`,
  )
  return data
}

export async function updateWorkspaceAISettings(
  workspaceId: string,
  patch: Partial<Pick<WorkspaceAISettings,
    'rag_enabled' | 'answer_model' | 'embedding_model' | 'rag_queries_per_day'>>,
) {
  const { data } = await api.put<WorkspaceAISettings>(
    `/intelligence/workspaces/${workspaceId}/ai-settings`,
    patch,
  )
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
