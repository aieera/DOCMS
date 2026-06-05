import { useMemo, useState } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import {
  GitBranch,
  AlertTriangle,
  Plus,
  Search,
  Pencil,
  Copy,
  Trash2,
  Workflow as WorkflowIcon,
  Lock,
  Unlock,
  UserPlus,
  MoreHorizontal,
} from 'lucide-react'

import {
  createWorkflowDefinition,
  deleteWorkflowDefinition,
  getWorkflowDefinitions,
  type ADR0073Step,
  type WorkflowDefinition,
} from '@/api/workflows'
import { readErrorMessage } from '@/api/client'
import { useSetWorkflowVisibility } from '@/hooks/useWorkflows'

import { PageHeader } from '@/components/shared/PageHeader'
import { Card } from '@/components/ui/card'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { Skeleton } from '@/components/ui/Skeleton'
import { ConfirmDialog } from '@/components/ui/shadcn/confirm-dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/shadcn/dropdown-menu'
import { presetFor } from '@/components/workflows/step-presets'
import { ManageWorkflowAccessDialog } from '@/components/workflows/ManageWorkflowAccessDialog'

// /workflows — Template library.
// Card grid of every workflow template the tenant has authored, with
// a search filter, a "New template" CTA that lands on the editor's
// new-sentinel route, and per-card actions (Edit / Duplicate / Delete).

function WorkflowsPage() {
  const { t } = useTranslation('workflows')
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [query, setQuery] = useState('')
  const [confirmDeleteId, setConfirmDeleteId] = useState<string | null>(null)
  // flipFor — definition currently in the shared/private confirm dialog
  // accessFor — definition currently in the Manage access dialog
  const [flipFor, setFlipFor] = useState<WorkflowDefinition | null>(null)
  const [accessFor, setAccessFor] = useState<WorkflowDefinition | null>(null)
  const setVisibility = useSetWorkflowVisibility()

  const defsQ = useQuery({
    queryKey: ['workflow-definitions'],
    queryFn: getWorkflowDefinitions,
    retry: 1,
  })

  const visible = useMemo(() => {
    const items = defsQ.data ?? []
    if (!query.trim()) return items
    const q = query.toLowerCase()
    return items.filter(
      (d) => d.name.toLowerCase().includes(q) || (d.description?.toLowerCase().includes(q) ?? false),
    )
  }, [defsQ.data, query])

  const duplicate = useAppMutation({
    mutationFn: (d: WorkflowDefinition) =>
      createWorkflowDefinition({
        name: `${d.name} (copy)`,
        description: d.description,
        steps: (d.steps as unknown as ADR0073Step[]) ?? [],
      }),
    onSuccess: (created) => {
      toast.success(t('editor.save_success'))
      qc.invalidateQueries({ queryKey: ['workflow-definitions'] })
      navigate({
        to: '/workflows/$templateId/edit',
        params: { templateId: created.id },
      })
    },
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? t('editor.save_error')),
  })

  const del = useAppMutation({
    mutationFn: (id: string) => deleteWorkflowDefinition(id),
    onSuccess: () => {
      toast.success(t('templates.delete_confirm.confirm'))
      setConfirmDeleteId(null)
      qc.invalidateQueries({ queryKey: ['workflow-definitions'] })
    },
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? t('editor.save_error')),
  })

  return (
    <div>
      <PageHeader
        title={t('templates.title')}
        description={t('templates.description')}
        actions={
          <Button
            onClick={() =>
              navigate({ to: '/workflows/$templateId/edit', params: { templateId: 'new' } })
            }
            data-testid="workflows-new-template"
          >
            <Plus className="me-1 h-4 w-4" />
            {t('templates.new_button')}
          </Button>
        }
      />

      <div className="mb-4">
        <div className="relative max-w-md">
          <Search className="pointer-events-none absolute start-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
          <Input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={t('templates.search_placeholder')}
            className="ps-9"
            data-testid="workflows-search"
          />
        </div>
      </div>

      {defsQ.isLoading ? (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          <Skeleton className="h-44" />
          <Skeleton className="h-44" />
          <Skeleton className="h-44" />
        </div>
      ) : defsQ.isError ? (
        <Card className="flex flex-col items-center justify-center gap-2 border-destructive/40 bg-destructive/5 p-12 text-center">
          <AlertTriangle className="h-10 w-10 text-destructive" />
          <h3 className="text-lg font-medium">Could not load templates</h3>
          <p className="max-w-md text-sm text-muted-foreground">
            {defsQ.error instanceof Error ? defsQ.error.message : 'Server error — please retry.'}
          </p>
          <Button onClick={() => defsQ.refetch()} loading={defsQ.isFetching}>
            Retry
          </Button>
        </Card>
      ) : visible.length === 0 ? (
        <Card className="flex flex-col items-center justify-center gap-3 border-dashed p-12 text-center">
          <span className="flex h-12 w-12 items-center justify-center rounded-full bg-primary/10 text-primary">
            <WorkflowIcon className="h-6 w-6" />
          </span>
          <h3 className="text-lg font-medium">{t('templates.empty_title')}</h3>
          <p className="max-w-md text-sm text-muted-foreground">
            {t('templates.empty_description')}
          </p>
          <Button
            onClick={() =>
              navigate({ to: '/workflows/$templateId/edit', params: { templateId: 'new' } })
            }
          >
            <Plus className="me-1 h-4 w-4" />
            {t('templates.new_button')}
          </Button>
        </Card>
      ) : (
        <ul
          className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3"
          data-testid="workflows-grid"
        >
          {visible.map((d) => (
            <TemplateCard
              key={d.id}
              def={d}
              onEdit={() =>
                navigate({ to: '/workflows/$templateId/edit', params: { templateId: d.id } })
              }
              onDuplicate={() => duplicate.mutate(d)}
              onDelete={() => setConfirmDeleteId(d.id)}
              onFlipVisibility={() => setFlipFor(d)}
              onManageAccess={d.visibility === 'private' ? () => setAccessFor(d) : undefined}
            />
          ))}
        </ul>
      )}

      <ConfirmDialog
        open={!!confirmDeleteId}
        onOpenChange={(o) => !o && setConfirmDeleteId(null)}
        title={t('templates.delete_confirm.title')}
        description={t('templates.delete_confirm.description')}
        confirmLabel={t('templates.delete_confirm.confirm')}
        destructive
        loading={del.isPending}
        onConfirm={() => confirmDeleteId && del.mutate(confirmDeleteId)}
      />

      {flipFor && (() => {
        const target: 'shared' | 'private' =
          flipFor.visibility === 'private' ? 'shared' : 'private'
        const keyBase =
          target === 'private'
            ? 'visibility.flip_to_private_confirm'
            : 'visibility.flip_to_shared_confirm'
        return (
          <ConfirmDialog
            open={!!flipFor}
            onOpenChange={(o) => !o && setFlipFor(null)}
            title={t(`${keyBase}.title`)}
            description={t(`${keyBase}.description`)}
            confirmLabel={t(`${keyBase}.confirm`)}
            onConfirm={() => {
              const wf = flipFor
              setFlipFor(null)
              setVisibility.mutate(
                { workflowId: wf.id, visibility: target },
                {
                  onSuccess: () => toast.success(t('toasts.visibility_changed')),
                  onError: (e: unknown) =>
                    toast.error(readErrorMessage(e) ?? t('toasts.error')),
                },
              )
            }}
          />
        )
      })()}

      {accessFor && (
        <ManageWorkflowAccessDialog
          open={!!accessFor}
          onOpenChange={(o) => !o && setAccessFor(null)}
          definition={accessFor}
        />
      )}
    </div>
  )
}

interface CardProps {
  def: WorkflowDefinition
  onEdit: () => void
  onDuplicate: () => void
  onDelete: () => void
  // Hands the flip back to the parent; the parent owns the confirm dialog.
  onFlipVisibility?: () => void
  // Only wired for private definitions; undefined hides the menu item.
  onManageAccess?: () => void
}

function TemplateCard({
  def,
  onEdit,
  onDuplicate,
  onDelete,
  onFlipVisibility,
  onManageAccess,
}: CardProps) {
  const { t } = useTranslation('workflows')
  const steps = (def.steps as unknown as ADR0073Step[]) ?? []
  const isPrivate = def.visibility === 'private'
  return (
    <li>
      <Card className="flex h-full flex-col p-4 transition-shadow hover:shadow-md">
        <div className="flex items-start gap-3">
          <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
            <GitBranch className="h-4 w-4" />
          </span>
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-1.5">
              <h3 className="truncate text-base font-semibold" title={def.name}>
                {def.name}
              </h3>
              {isPrivate && (
                <span
                  className="inline-flex items-center gap-0.5 rounded-full bg-warning/15 px-1.5 py-0 text-[10px] font-medium text-warning"
                  title={t('templates.private_tooltip')}
                >
                  <Lock className="h-3 w-3" />
                  {t('templates.private_label')}
                </span>
              )}
            </div>
            <p className="mt-0.5 text-xs text-muted-foreground">
              {t('templates.card.steps_count', {
                count: steps.length,
                defaultValue: steps.length === 1 ? '1 step' : `${steps.length} steps`,
              })}
            </p>
          </div>
          {(onFlipVisibility || onManageAccess) && (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-7 w-7 shrink-0 text-muted-foreground"
                  aria-label={t('templates.actions_aria_label')}
                  data-testid={`tpl-menu-${def.id}`}
                >
                  <MoreHorizontal className="h-4 w-4" />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                {onFlipVisibility && (
                  <DropdownMenuItem
                    onSelect={onFlipVisibility}
                    data-testid={`tpl-flip-visibility-${def.id}`}
                  >
                    {isPrivate ? (
                      <>
                        <Unlock className="me-2 h-4 w-4" />
                        {t('templates.card.make_shared')}
                      </>
                    ) : (
                      <>
                        <Lock className="me-2 h-4 w-4" />
                        {t('templates.card.make_private')}
                      </>
                    )}
                  </DropdownMenuItem>
                )}
                {onManageAccess && (
                  <>
                    <DropdownMenuSeparator />
                    <DropdownMenuItem
                      onSelect={onManageAccess}
                      data-testid={`tpl-manage-access-${def.id}`}
                    >
                      <UserPlus className="me-2 h-4 w-4" />
                      {t('templates.card.manage_access')}
                    </DropdownMenuItem>
                  </>
                )}
              </DropdownMenuContent>
            </DropdownMenu>
          )}
        </div>

        {def.description && (
          <p className="mt-3 line-clamp-2 text-sm text-muted-foreground">{def.description}</p>
        )}

        <div className="mt-3 flex flex-wrap gap-1">
          {steps.slice(0, 6).map((s) => {
            const preset = presetFor(s.type)
            const Icon = preset.icon
            return (
              <span
                key={s.id}
                title={`${s.name || s.type} — ${s.id}`}
                className={`inline-flex items-center gap-1 rounded-md border px-1.5 py-0.5 text-[10px] uppercase tracking-wide ${preset.toneCls}`}
              >
                <Icon className="h-3 w-3" />
                {t(`editor.step.${s.type}`)}
              </span>
            )
          })}
          {steps.length > 6 && (
            <span className="inline-flex items-center rounded-md border border-border bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">
              +{steps.length - 6}
            </span>
          )}
        </div>

        <div className="mt-auto flex items-center justify-end gap-1 pt-3">
          <Button size="sm" variant="ghost" onClick={onEdit} data-testid={`tpl-edit-${def.id}`}>
            <Pencil className="me-1 h-3.5 w-3.5" />
            {t('templates.card.edit')}
          </Button>
          <Button
            size="sm"
            variant="ghost"
            onClick={onDuplicate}
            data-testid={`tpl-duplicate-${def.id}`}
          >
            <Copy className="me-1 h-3.5 w-3.5" />
            {t('templates.card.duplicate')}
          </Button>
          <Button
            size="sm"
            variant="ghost"
            onClick={onDelete}
            className="text-destructive hover:bg-destructive/10 hover:text-destructive"
            data-testid={`tpl-delete-${def.id}`}
          >
            <Trash2 className="me-1 h-3.5 w-3.5" />
            {t('templates.card.delete')}
          </Button>
        </div>
      </Card>
    </li>
  )
}

export const Route = createFileRoute('/_authenticated/workflows/')({ component: WorkflowsPage })
