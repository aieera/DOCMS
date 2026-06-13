// Document processing status (ADR 0115). The intelligence workers record
// per-stage status into document_processing_stages; this surfaces it so a
// failed/stuck pipeline shows "processing failed because X, retry" instead
// of empty tabs.
import { api } from './client'

export type StageStatus = 'pending' | 'running' | 'completed' | 'failed' | 'skipped'
export type ProcessingRollup = 'pending' | 'running' | 'completed' | 'failed' | 'partial'

export interface ProcessingStage {
  stage: string
  status: StageStatus
  attempts: number
  failure_reason?: string
  failure_detail?: string
  started_at?: string
  completed_at?: string
}

export interface ProcessingStatus {
  status: ProcessingRollup
  stages: ProcessingStage[]
}

export async function getProcessingStatus(documentId: string): Promise<ProcessingStatus> {
  const { data } = await api.get<ProcessingStatus>(`/documents/${documentId}/processing`)
  // The Go handler omits an empty stages slice (nil → no key); normalize.
  return { status: data.status ?? 'pending', stages: data.stages ?? [] }
}

export async function reprocessDocument(documentId: string): Promise<void> {
  await api.post(`/documents/${documentId}/reprocess`)
}

// Maps the ADR 0115 failure-reason taxonomy to user-facing copy.
export function failureReasonLabel(reason?: string): string {
  switch (reason) {
    case 'unsupported_mime': return 'This file type can’t be processed.'
    case 'file_corrupted': return 'The file couldn’t be read — it may be corrupted or truncated.'
    case 'dependency_unavailable': return 'A processing service was temporarily unavailable.'
    case 'dependency_quota': return 'A processing quota or rate limit was reached.'
    case 'timeout': return 'Processing took too long and timed out.'
    case 'decrypt_failed': return 'The stored file couldn’t be decrypted for processing.'
    case 'event_publish_failed': return 'Processing finished but couldn’t notify the next step.'
    case 'worker_crash': return 'A processing worker stopped unexpectedly.'
    case 'policy_denied': return 'This processing step is disabled by your tenant policy.'
    default: return 'Processing failed for an unknown reason.'
  }
}
