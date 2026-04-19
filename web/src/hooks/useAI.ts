import { useMutation } from '@tanstack/react-query'
import { askQuestion, summarizeDocument, detectRedactions } from '@/api/intelligence'

export function useAskQuestion() {
  return useMutation({ mutationFn: ({ question, scope, scopeId }: { question: string; scope?: string; scopeId?: string }) => askQuestion(question, scope, scopeId) })
}

export function useSummarize() {
  return useMutation({ mutationFn: ({ docId, text, length }: { docId: string; text: string; length?: string }) => summarizeDocument(docId, text, length) })
}

export function useDetectRedactions() {
  return useMutation({ mutationFn: ({ docId, versionId, text }: { docId: string; versionId: string; text: string }) => detectRedactions(docId, versionId, text) })
}
