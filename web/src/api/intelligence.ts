import { api } from './client'
import type { AIAnswer } from '@/types/api'

export async function askQuestion(question: string, scope?: string, scopeId?: string) {
  const { data } = await api.post<AIAnswer>('/intelligence/ask', { question, scope, scope_id: scopeId })
  return data
}

export async function summarizeDocument(documentId: string, text: string, length = 'medium') {
  const { data } = await api.post('/intelligence/summarize', { document_id: documentId, text, length })
  return data
}

export async function detectRedactions(documentId: string, versionId: string, text: string) {
  const { data } = await api.post('/intelligence/redact/detect', { document_id: documentId, version_id: versionId, text })
  return data
}
