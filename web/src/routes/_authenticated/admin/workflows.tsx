import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { GitBranch, Plus, ExternalLink, X } from 'lucide-react'

import {
  cancelWorkflowInstance,
  getWorkflowDefinitions,
  listActiveInstances,
  type WorkflowInstance,
} from '@/api/workflows'
import { readErrorMessage } from '@/api/client'

import { PageHeader } from '@/components/shared/PageHeader'
import { Card } from '@/components/ui/card'
import { Button } from '@/components/ui/shadcn/button'
import { Skeleton } from '@/components/ui/Skeleton'
import { ConfirmDialog } from '@/components/ui/shadcn/confirm-dialog'
import { WorkflowStatusBadge } from '@/components/workflows/WorkflowStatusBadge'
import { formatDateTime, formatShortId } from '@/lib/formatters'

// /admin/workflows — tenant-wide workflow administration.
// Top section: every template the tenant has authored (jumps to
// /workflows for the full library + editor — admin doesn't need a
// duplicate library here, just a quick-look).
// Bottom section: every active (non-terminal) workflow instance with
// document, template, step + cancel action.

function WorkflowsAdminPage() {
  const { t } = useTranslation('workflows')
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [confirmCancelId, setConfirmCancelId] = useState<string | null>(null)

  const templates = useQuery({
    queryKey: ['workflow-definitions'],
    queryFn: getWorkflowDefinitions,
  })

  const instances = useQuery({
    queryKey: ['workflow-instances', 'active'],
    queryFn: listActiveInstances,
    refetchInterval: 15_000,
  })

  const cancel = useAppMutation({
    mutationFn: (id: string) => cancelWorkflowInstance(id),
    onSuccess: () => {
      toast.success(t('tab.toasts.cancelled'))
      setConfirmCancelId(null)
      qc.invalidateQueries({ queryKey: ['workflow-instances', 'active'] })
    },
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? t('tab.toasts.error')),
  })

  const templateNameById = new Map<string, string>(
    (templates.data ?? []).map((d) => [d.id, d.name]),
  )

  return (
    <div className="space-y-6">
      <PageHeader
        title={t('admin.title')}
        description={t('admin.description')}
        actions={
          <Button
            onClick={() =>
              navigate({ to: '/workflows/$templateId/edit', params: { templateId: 'new' } })
            }
          >
            <Plus className="me-1 h-4 w-4" />
            {t('templates.new_button')}
          </Button>
        }
      />

      <section>
        <header className="mb-3 flex items-center justify-between">
          <h2 className="text-lg font-semibold">{t('admin.templates_section')}</h2>
          <Button variant="ghost" size="sm" asChild>
            <Link to="/workflows" data-testid="admin-workflows-open-library">
              <ExternalLink className="me-1 h-3.5 w-3.5" />
              {t('templates.title')}
            </Link>
          </Button>
        </header>
        {templates.isLoading ? (
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
            <Skeleton className="h-24" />
            <Skeleton className="h-24" />
            <Skeleton className="h-24" />
          </div>
        ) : (templates.data ?? []).length === 0 ? (
          <Card className="flex flex-col items-center justify-center gap-2 border-dashed p-8 text-center">
            <GitBranch className="h-8 w-8 text-muted-foreground" />
            <p className="text-sm font-medium">{t('templates.empty_title')}</p>
            <p className="max-w-md text-xs text-muted-foreground">
              {t('templates.empty_description')}
            </p>
          </Card>
        ) : (
          <ul className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3" data-testid="admin-workflow-templates">
            {(templates.data ?? []).map((d) => (
              <li key={d.id}>
                <Link
                  to="/workflows/$templateId/edit"
                  params={{ templateId: d.id }}
                  className="block"
                >
                  <Card className="flex h-full items-start gap-3 p-3 transition-shadow hover:shadow-neu">
                    <span className="flex h-8 w-8 items-center justify-center rounded-md bg-primary/10 text-primary">
                      <GitBranch className="h-4 w-4" />
                    </span>
                    <div className="min-w-0 flex-1">
                      <p className="truncate text-sm font-semibold">{d.name}</p>
                      <p className="mt-0.5 text-[10px] text-muted-foreground">
                        {t('templates.card.steps_count', {
                          count: d.steps?.length ?? 0,
                          defaultValue:
                            (d.steps?.length ?? 0) === 1 ? '1 step' : `${d.steps?.length ?? 0} steps`,
                        })}
                      </p>
                    </div>
                  </Card>
                </Link>
              </li>
            ))}
          </ul>
        )}
      </section>

      <section>
        <header className="mb-3">
          <h2 className="text-lg font-semibold">{t('admin.instances_section')}</h2>
        </header>
        {instances.isLoading ? (
          <Skeleton className="h-40" />
        ) : (instances.data ?? []).length === 0 ? (
          <Card className="p-6 text-center text-sm text-muted-foreground">
            {t('admin.instances_empty')}
          </Card>
        ) : (
          <Card className="overflow-x-auto p-0">
            <table className="w-full text-sm" data-testid="admin-active-instances">
              <thead className="border-b border-border bg-muted/30 text-xs uppercase text-muted-foreground">
                <tr>
                  <th className="px-3 py-2 text-start font-medium">{t('admin.columns.document')}</th>
                  <th className="px-3 py-2 text-start font-medium">{t('admin.columns.template')}</th>
                  <th className="px-3 py-2 text-start font-medium">{t('admin.columns.status')}</th>
                  <th className="px-3 py-2 text-start font-medium">{t('admin.columns.current_step')}</th>
                  <th className="px-3 py-2 text-start font-medium">{t('admin.columns.started_at')}</th>
                  <th className="px-3 py-2 text-end font-medium">{t('admin.columns.actions')}</th>
                </tr>
              </thead>
              <tbody>
                {(instances.data ?? []).map((inst) => (
                  <InstanceRow
                    key={inst.id}
                    inst={inst}
                    templateName={templateNameById.get(inst.definition_id) ?? formatShortId('def', inst.definition_id)}
                    onCancel={() => setConfirmCancelId(inst.id)}
                  />
                ))}
              </tbody>
            </table>
          </Card>
        )}
      </section>

      <ConfirmDialog
        open={!!confirmCancelId}
        onOpenChange={(o) => !o && setConfirmCancelId(null)}
        title={t('tab.cancel_confirm.title')}
        description={t('tab.cancel_confirm.description')}
        confirmLabel={t('tab.cancel_confirm.confirm')}
        destructive
        loading={cancel.isPending}
        onConfirm={() => confirmCancelId && cancel.mutate(confirmCancelId)}
      />
    </div>
  )
}

function InstanceRow({
  inst,
  templateName,
  onCancel,
}: {
  inst: WorkflowInstance
  templateName: string
  onCancel: () => void
}) {
  const { t } = useTranslation('workflows')
  return (
    <tr className="border-b border-border last:border-0 hover:bg-muted/30">
      <td className="px-3 py-3 font-mono text-xs">
        {inst.document_id ? formatShortId('doc', inst.document_id) : '—'}
      </td>
      <td className="px-3 py-3">{templateName}</td>
      <td className="px-3 py-3">
        <WorkflowStatusBadge status={inst.status} />
      </td>
      <td className="px-3 py-3 text-muted-foreground">#{inst.current_step + 1}</td>
      <td className="px-3 py-3 text-muted-foreground">{formatDateTime(inst.created_at)}</td>
      <td className="px-3 py-3 text-end">
        <Button
          size="sm"
          variant="ghost"
          onClick={onCancel}
          className="text-destructive hover:bg-destructive/10 hover:text-destructive"
          data-testid={`admin-cancel-${inst.id}`}
        >
          <X className="me-1 h-3.5 w-3.5" />
          {t('admin.cancel')}
        </Button>
      </td>
    </tr>
  )
}

export const Route = createFileRoute('/_authenticated/admin/workflows')({
  component: WorkflowsAdminPage,
})
