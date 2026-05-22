import { askQuestion, summarizeDocument, detectRedactions } from '@/api/intelligence'
import { useAppMutation } from './useAppMutation'

// Wave 5 pattern 1: three AI mutations with NO onError before.
// 503 from a missing API key, 400 from a too-long prompt, 504 from
// LLM timeout — all vanished silently. No onSuccess to preserve on
// any of these (consumers read the mutation result directly).

export function useAskQuestion() {
  return useAppMutation({
    mutationFn: ({ question, scope, scopeId }: { question: string; scope?: string; scopeId?: string }) =>
      askQuestion(question, scope, scopeId),
    defaultErrorMessage: 'Could not ask the AI assistant',
  })
}

export function useSummarize() {
  return useAppMutation({
    mutationFn: ({ docId, text, length }: { docId: string; text: string; length?: string }) =>
      summarizeDocument(docId, text, length),
    defaultErrorMessage: 'Could not summarize document',
  })
}

export function useDetectRedactions() {
  return useAppMutation({
    mutationFn: ({ docId, versionId, text }: { docId: string; versionId: string; text: string }) =>
      detectRedactions(docId, versionId, text),
    defaultErrorMessage: 'Could not detect redaction candidates',
  })
}
