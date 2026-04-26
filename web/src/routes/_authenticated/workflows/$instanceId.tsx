// Per-instance workflow detail. Linked to from /tasks (each pending task
// has an instance_id) and from doc detail's future "active workflows"
// surface. Shows the running step, full definition graph for context,
// and a Cancel button gated to the initiator + admin/owner.
//
// Backend: GET /workflows/instances/{id} → WorkflowInstance,
// POST /workflows/instances/{id}/cancel. Definitions come from the
// existing list endpoint — there's no per-id getter, so we filter
// client-side; the list is small (one row per saved template).

import { createFileRoute, Link } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { useState } from 'react'
import { ArrowLeft, FileText, GitBranch, X } from 'lucide-react'

import {
  cancelWorkflowInstance,
  getWorkflowDefinitions,
  getWorkflowInstance,
  type WorkflowDefinition,
} from '@/api/workflows'
import { useDocument } from '@/hooks/useDocuments'
import { useAuthStore } from '@/store/authStore'
import { Badge } from '@/components/ui/Badge'
import { Button } from '@/components/ui/Button'
import { ConfirmDialog } from '@/components/ui/ConfirmDialog'
import { Skeleton } from '@/components/ui/Skeleton'
import { PageHeader } from '@/components/shared/PageHeader'
import { WorkflowGraph } from '@/components/shared/WorkflowGraph'
import { formatDateTime } from '@/lib/formatters'

function statusVariant(s: string): string {
  switch (s) {
    case 'running':
      return 'in_review'
    case 'completed':
      return 'active'
    case 'rejected':
    case 'cancelled':
      return 'disposed'
    default:
      return 'default'
  }
}

function InstanceDetailPage() {
  const { instanceId } = Route.useParams()
  const qc = useQueryClient()

  const userId = useAuthStore((s) => s.user?.id)
  const userRole = useAuthStore((s) => s.user?.role)

  const { data: inst, isLoading: instLoading, error } = useQuery({
    queryKey: ['workflow-instance', instanceId],
    queryFn: () => getWorkflowInstance(instanceId),
  })
  const { data: defs } = useQuery({
    queryKey: ['workflow-definitions'],
    queryFn: getWorkflowDefinitions,
  })
  const { data: doc } = useDocument(inst?.document_id ?? '')

  const def: WorkflowDefinition | undefined = defs?.find((d) => d.id === inst?.definition_id)

  const [confirmOpen, setConfirmOpen] = useState(false)
  const cancel = useMutation({
    mutationFn: () => cancelWorkflowInstance(instanceId),
    onSuccess: () => {
      toast.success('Workflow cancelled')
      qc.invalidateQueries({ queryKey: ['workflow-instance', instanceId] })
      qc.invalidateQueries({ queryKey: ['workflow-tasks'] })
      setConfirmOpen(false)
    },
    onError: (e: unknown) => {
      const anyErr = e as { response?: { data?: { message?: string } }; message?: string }
      toast.error(anyErr?.response?.data?.message ?? anyErr?.message ?? 'Cancel failed')
    },
  })

  if (instLoading) {
    return (
      <div>
        <PageHeader title="Workflow" description="" />
        <Skeleton className="h-64" />
      </div>
    )
  }

  if (error || !inst) {
    return (
      <div>
        <PageHeader title="Workflow not found" description="" />
        <p className="text-sm text-[var(--color-text-secondary)]">
          This workflow instance doesn't exist or you don't have access to it.
        </p>
        <Link to="/tasks" className="mt-4 inline-flex items-center gap-1 text-sm text-[var(--color-primary)] hover:underline">
          <ArrowLeft className="h-3.5 w-3.5" /> Back to tasks
        </Link>
      </div>
    )
  }

  // Cancel is gated to the initiator or any admin/owner. Backend
  // re-checks; UI only hides the button when we're sure it would 403.
  const canCancel =
    inst.status === 'running' &&
    (inst.initiated_by === userId || userRole === 'admin' || userRole === 'owner')

  return (
    <div>
      <PageHeader
        title={def?.name ?? 'Workflow Instance'}
        description={def?.description ?? `Instance ${instanceId.slice(0, 8)}…`}
        actions={
          canCancel ? (
            <Button variant="destructive" onClick={() => setConfirmOpen(true)} loading={cancel.isPending}>
              <X className="h-4 w-4" /> Cancel workflow
            </Button>
          ) : undefined
        }
      />

      <div className="mb-6 grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Field label="Status">
          <Badge variant={statusVariant(inst.status)}>{inst.status}</Badge>
        </Field>
        <Field label="Current step">
          <span className="text-sm font-medium">
            {def?.steps[inst.current_step]?.name ?? `Step ${inst.current_step + 1}`}
            {def?.steps && (
              <span className="ml-1 text-xs text-[var(--color-text-secondary)]">
                ({inst.current_step + 1} of {def.steps.length})
              </span>
            )}
          </span>
        </Field>
        <Field label="Started">
          <span className="text-sm">{formatDateTime(inst.created_at)}</span>
        </Field>
        <Field label={inst.completed_at ? 'Completed' : 'Document'}>
          {inst.completed_at ? (
            <span className="text-sm">{formatDateTime(inst.completed_at)}</span>
          ) : doc ? (
            <Link
              to="/workspaces/$workspaceId/documents/$documentId"
              params={{ workspaceId: doc.workspace_id, documentId: doc.id }}
              className="inline-flex items-center gap-1 text-sm text-[var(--color-primary)] hover:underline"
            >
              <FileText className="h-3.5 w-3.5" /> {doc.title}
            </Link>
          ) : (
            <span className="text-sm text-[var(--color-text-secondary)]">{inst.document_id.slice(0, 8)}…</span>
          )}
        </Field>
      </div>

      <section>
        <h2 className="mb-2 flex items-center gap-2 text-sm font-semibold">
          <GitBranch className="h-4 w-4" aria-hidden="true" /> Definition
        </h2>
        {def ? (
          <WorkflowGraph steps={def.steps} />
        ) : (
          <p className="text-xs text-[var(--color-text-secondary)]">
            Definition {inst.definition_id.slice(0, 8)}… is no longer available.
          </p>
        )}
      </section>

      {inst.temporal_run_id && (
        <p className="mt-4 font-mono text-[10px] text-[var(--color-text-secondary)]">
          temporal run: {inst.temporal_run_id}
        </p>
      )}

      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title="Cancel this workflow?"
        description="In-flight tasks for assignees will be marked cancelled. This action is recorded in the audit trail and cannot be undone."
        confirmLabel="Cancel workflow"
        destructive
        loading={cancel.isPending}
        onConfirm={() => cancel.mutate()}
      />
    </div>
  )
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-3">
      <div className="text-xs uppercase tracking-wide text-[var(--color-text-secondary)]">{label}</div>
      <div className="mt-1">{children}</div>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/workflows/$instanceId')({
  component: InstanceDetailPage,
})
