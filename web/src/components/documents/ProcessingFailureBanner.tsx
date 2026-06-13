// ADR 0115 — surfaces a failed/partial intelligence pipeline on the
// document detail page. Without this, a dropped OCR/classify/embed job
// leaves every tab empty with no explanation or recovery path.
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { AlertCircle, RefreshCw } from 'lucide-react'
import { toast } from 'sonner'
import { Card } from '@/components/ui/card'
import {
  getProcessingStatus,
  reprocessDocument,
  failureReasonLabel,
  type ProcessingStage,
} from '@/api/processing'
import { readErrorMessage } from '@/api/client'

export function ProcessingFailureBanner({
  documentId,
  canRetry,
}: {
  documentId: string
  // Reprocess is role-gated server-side (owner|admin|compliance_officer);
  // mirror it client-side so members see the explanation but not a button
  // that would 403.
  canRetry: boolean
}) {
  const qc = useQueryClient()
  const { data } = useQuery({
    queryKey: ['processing', documentId],
    queryFn: () => getProcessingStatus(documentId),
    // Poll while running so the banner clears itself once a retry finishes.
    refetchInterval: (q) =>
      q.state.data?.status === 'running' ? 4000 : false,
  })

  const retry = useMutation({
    mutationFn: () => reprocessDocument(documentId),
    onSuccess: () => {
      toast.success('Reprocessing started')
      qc.invalidateQueries({ queryKey: ['processing', documentId] })
    },
    onError: (e) => toast.error(readErrorMessage(e) ?? 'Could not start reprocessing'),
  })

  if (!data || (data.status !== 'failed' && data.status !== 'partial')) return null

  const failed = data.stages.filter((s) => s.status === 'failed')

  return (
    <Card
      className="flex flex-wrap items-start justify-between gap-3 border-destructive/40 bg-destructive/5 p-4"
      data-testid="processing-failure-banner"
    >
      <div className="flex items-start gap-2">
        <AlertCircle className="mt-0.5 h-4 w-4 shrink-0 text-destructive" />
        <div className="space-y-1">
          <p className="text-sm font-medium text-destructive">
            {data.status === 'partial' ? 'Some processing steps failed' : 'Processing failed'}
          </p>
          <ul className="space-y-0.5 text-xs text-muted-foreground">
            {failed.length === 0 && <li>The processing pipeline did not complete.</li>}
            {failed.map((s: ProcessingStage) => (
              <li key={s.stage}>
                <span className="font-medium capitalize">{s.stage.replace(/_/g, ' ')}</span>
                {' — '}
                {failureReasonLabel(s.failure_reason)}
              </li>
            ))}
          </ul>
        </div>
      </div>
      {canRetry && (
        <button
          type="button"
          onClick={() => retry.mutate()}
          disabled={retry.isPending}
          className="inline-flex items-center gap-1.5 rounded-lg border border-destructive/40 bg-background px-3 py-1.5 text-sm font-medium text-destructive transition-colors hover:bg-destructive/10 disabled:opacity-50"
          data-testid="reprocess-button"
        >
          <RefreshCw className={`h-3.5 w-3.5 ${retry.isPending ? 'animate-spin' : ''}`} />
          {retry.isPending ? 'Retrying…' : 'Retry'}
        </button>
      )}
    </Card>
  )
}
