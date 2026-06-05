import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { GitBranch, PlayCircle, X } from 'lucide-react'

import {
  actOnStep,
  attachWorkflowToDocument,
  cancelWorkflowInstance,
  getDocumentWorkflow,
  getWorkflowDefinitions,
  type WorkflowDefinition,
  type WorkflowInstance,
  type WorkflowTask,
} from '@/api/workflows'
import { useAuthStore } from '@/store/authStore'
import { Card } from '@/components/ui/card'
import { Button } from '@/components/ui/shadcn/button'
import { Spinner } from '@/components/ui/Spinner'
import { ConfirmDialog } from '@/components/ui/shadcn/confirm-dialog'
import { readErrorMessage } from '@/api/client'
import { cn } from '@/lib/cn'
import { WorkflowStatusBadge } from './WorkflowStatusBadge'
import { WorkflowTimeline } from './WorkflowTimeline'
import { StartWorkflowPicker } from './StartWorkflowPicker'

interface Props {
  documentId: string
  // canCancel = current user owns the workflow or is an admin.
  // Resolved by the parent so this component stays role-agnostic.
  canCancel?: boolean
}

// WorkflowTab is the heart of the new document-associated workflow
// surface. Three modes:
//   1. Loading       — initial fetch.
//   2. No instance   — empty state + Start a workflow button that
//                       opens StartWorkflowPicker.
//   3. Has instance  — header status banner + WorkflowTimeline with
//                       inline action buttons for the assigned step
//                       + Cancel workflow (when canCancel).
//
// All mutations invalidate the bundle query so the timeline + status
// stay in sync without optimistic updates that would have to be
// inverted on backend rejection.
export function WorkflowTab({ documentId, canCancel = false }: Props) {
  const { t } = useTranslation('workflows')
  const qc = useQueryClient()
  const viewer = useAuthStore((s) => s.user)
  const [pickerOpen, setPickerOpen] = useState(false)
  const [confirmCancelOpen, setConfirmCancelOpen] = useState(false)

  const bundle = useQuery({
    queryKey: ['workflow-instance', documentId],
    queryFn: () => getDocumentWorkflow(documentId),
    enabled: Boolean(documentId),
    // 5 s refetch on focus + 30 s in the background so the timeline
    // converges with Temporal callbacks without making the UI feel
    // chatty.
    refetchInterval: (q) => {
      const status = q.state.data?.instance?.status
      return status === 'running' || status === 'pending' ? 5_000 : 30_000
    },
  })

  // Templates loaded lazily — only when the user opens the picker.
  // No-instance docs are common so we don't want to fetch templates
  // for every doc-detail page mount.
  const templates = useQuery({
    queryKey: ['workflow-definitions'],
    queryFn: getWorkflowDefinitions,
    enabled: pickerOpen,
    staleTime: 60_000,
  })

  const attach = useAppMutation({
    mutationFn: (templateId: string) => attachWorkflowToDocument(documentId, templateId),
    onSuccess: () => {
      toast.success(t('tab.toasts.started'))
      setPickerOpen(false)
      qc.invalidateQueries({ queryKey: ['workflow-instance', documentId] })
    },
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? t('tab.toasts.error')),
  })

  const cancel = useAppMutation({
    mutationFn: () => cancelWorkflowInstance(bundle.data!.instance.id),
    onSuccess: () => {
      toast.success(t('tab.toasts.cancelled'))
      setConfirmCancelOpen(false)
      qc.invalidateQueries({ queryKey: ['workflow-instance', documentId] })
    },
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? t('tab.toasts.error')),
  })

  const act = useAppMutation({
    mutationFn: ({
      stepIndex,
      action,
      comment,
    }: {
      stepIndex: number
      action: 'approve' | 'reject' | 'sign' | 'delegate'
      comment: string
    }) =>
      actOnStep(bundle.data!.instance.id, stepIndex, action, {
        comment: comment.trim() || undefined,
      }),
    onSuccess: (_d, vars) => {
      const key =
        vars.action === 'reject'
          ? 'rejected'
          : vars.action === 'sign'
            ? 'signed'
            : vars.action === 'delegate'
              ? 'delegated'
              : 'approved'
      toast.success(t(`tab.toasts.${key}`))
      qc.invalidateQueries({ queryKey: ['workflow-instance', documentId] })
    },
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? t('tab.toasts.error')),
  })

  if (bundle.isLoading) {
    return (
      <div className="flex justify-center py-12">
        <Spinner className="h-6 w-6" />
      </div>
    )
  }

  if (!bundle.data) {
    return (
      <>
        <Card className="flex flex-col items-center gap-3 p-12 text-center">
          <span className="flex h-12 w-12 items-center justify-center rounded-full bg-primary/10 text-primary">
            <GitBranch className="h-6 w-6" />
          </span>
          <div>
            <p className="text-base font-semibold">{t('tab.empty_title')}</p>
            <p className="mt-1 text-sm text-muted-foreground">{t('tab.empty_description')}</p>
          </div>
          <Button onClick={() => setPickerOpen(true)} data-testid="workflow-tab-start">
            <PlayCircle className="me-1.5 h-4 w-4" />
            {t('tab.start_button')}
          </Button>
        </Card>
        <StartWorkflowPicker
          open={pickerOpen}
          onOpenChange={setPickerOpen}
          templates={templates.data ?? []}
          isLoading={templates.isLoading}
          onPick={(id) => attach.mutate(id)}
          isStarting={attach.isPending}
        />
      </>
    )
  }

  const { instance, tasks } = bundle.data
  const banner = bannerText(instance, tasks, t)

  // The current step type drives whether the action button shows as
  // Sign vs Approve. We don't have the full definition in the bundle,
  // so derive from the task step_name heuristics. Falls back to
  // 'approval' so non-signature steps render normally.
  const stepTypeByIndex: Record<number, string> = {}
  tasks.forEach((task, idx) => {
    stepTypeByIndex[idx] = /sign/i.test(task.step_name) ? 'signature' : 'approval'
  })

  return (
    <div className="space-y-4">
      <Card
        className={cn(
          'flex flex-wrap items-center justify-between gap-3 p-3',
          instance.status === 'running' && 'border-primary/40 bg-primary/5',
        )}
      >
        <div className="flex items-center gap-2">
          <WorkflowStatusBadge status={instance.status} />
          <span className="text-sm text-muted-foreground">{banner}</span>
        </div>
        {canCancel && instance.status === 'running' && (
          <Button
            variant="outline"
            size="sm"
            onClick={() => setConfirmCancelOpen(true)}
            data-testid="workflow-tab-cancel"
          >
            <X className="me-1 h-3.5 w-3.5" />
            {t('tab.cancel_button')}
          </Button>
        )}
      </Card>

      <WorkflowTimeline
        tasks={tasks}
        currentStepIndex={instance.current_step}
        viewerId={viewer?.id ?? ''}
        stepTypeByIndex={stepTypeByIndex}
        onAct={(task, action, comment) => {
          const idx = tasks.findIndex((x) => x.id === task.id)
          act.mutate({ stepIndex: idx >= 0 ? idx : instance.current_step, action, comment })
        }}
        isActing={act.isPending}
      />

      <ConfirmDialog
        open={confirmCancelOpen}
        onOpenChange={setConfirmCancelOpen}
        title={t('tab.cancel_confirm.title')}
        description={t('tab.cancel_confirm.description')}
        confirmLabel={t('tab.cancel_confirm.confirm')}
        destructive
        loading={cancel.isPending}
        onConfirm={() => cancel.mutate()}
      />
    </div>
  )
}

function bannerText(
  instance: WorkflowInstance,
  tasks: WorkflowTask[],
  t: ReturnType<typeof useTranslation>['t'],
): string {
  if (instance.status === 'running') {
    const current = tasks[instance.current_step] ?? tasks.find((tt) => tt.status === 'pending')
    return t('tab.banner.running', { step_name: current?.step_name ?? '—' })
  }
  return t(`tab.banner.${instance.status}`)
}

// Re-export the picker + definitions list so other surfaces (admin)
// can use the same components without re-importing through the
// folder index dance.
export { useDocumentWorkflowQueryKey }

function useDocumentWorkflowQueryKey(documentId: string) {
  return useMemo(() => ['workflow-instance', documentId] as const, [documentId])
}

// Re-export WorkflowDefinition for parent imports
export type { WorkflowDefinition }
