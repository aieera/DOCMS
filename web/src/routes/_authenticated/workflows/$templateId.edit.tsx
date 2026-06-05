import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useEffect, useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { ArrowLeft, CheckCircle2, AlertTriangle, Save } from 'lucide-react'

import { PageHeader } from '@/components/shared/PageHeader'
import { Card } from '@/components/ui/card'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { Textarea } from '@/components/ui/shadcn/textarea'
import { Spinner } from '@/components/ui/Spinner'
import { readErrorMessage } from '@/api/client'
import {
  createWorkflowDefinition,
  getWorkflowDefinition,
  updateWorkflowDefinition,
  type ADR0073Step,
  type WorkflowDefinition,
} from '@/api/workflows'

import { StepChainList } from '@/components/workflows/StepChainList'
import { StepConfigPanel } from '@/components/workflows/StepConfigPanel'
import { StepPresetMenu } from '@/components/workflows/StepPresetMenu'
import { issuesByStep, validateSteps } from '@/components/workflows/validate'
import { newStepFrom, presetFor, type StepType } from '@/components/workflows/step-presets'

// /workflows/$templateId/edit
//
// Sentinel: templateId === 'new' opens a blank editor that calls
// createWorkflowDefinition on save. Otherwise we GET the template,
// hydrate the form, and PUT on save.
//
// 3-pane layout (responsive: lg+ shows all three, smaller stacks):
//   left   = template name + description + Save + Validation chip
//   center = StepChainList + StepPresetMenu
//   right  = StepConfigPanel for the selected step (or empty)

function TemplateEditorPage() {
  const { templateId } = Route.useParams()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const { t } = useTranslation('workflows')
  const isNew = templateId === 'new'

  const defQ = useQuery({
    queryKey: ['workflow-definition', templateId],
    queryFn: () => getWorkflowDefinition(templateId),
    enabled: !isNew,
  })

  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [steps, setSteps] = useState<ADR0073Step[]>([])
  const [selectedId, setSelectedId] = useState<string | null>(null)

  // Hydrate from the fetched template once it lands. The dependency
  // is the raw query data so an external refetch (e.g. another
  // editor saving) reflects here.
  useEffect(() => {
    if (defQ.data) {
      setName(defQ.data.name)
      setDescription(defQ.data.description ?? '')
      // Backend ships nested OR flat shape depending on when the
      // definition was written. Normalise to ADR0073Step before
      // letting the editor touch it.
      setSteps(normalizeToFlatSteps(defQ.data))
    }
  }, [defQ.data])

  const issues = useMemo(() => validateSteps(steps), [steps])
  const issuesByStepId = useMemo(() => issuesByStep(issues), [issues])
  const issueIds = useMemo(() => new Set(issues.map((i) => i.stepId)), [issues])
  const selected = steps.find((s) => s.id === selectedId) ?? null

  const save = useAppMutation({
    mutationFn: async () => {
      const body = { name, description, steps }
      if (isNew) {
        return createWorkflowDefinition(body)
      }
      return updateWorkflowDefinition(templateId, body)
    },
    onSuccess: (def) => {
      toast.success(t('editor.save_success'))
      qc.invalidateQueries({ queryKey: ['workflow-definitions'] })
      qc.invalidateQueries({ queryKey: ['workflow-definition', def.id] })
      if (isNew) {
        navigate({ to: '/workflows/$templateId/edit', params: { templateId: def.id }, replace: true })
      }
    },
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? t('editor.save_error')),
  })

  const addStep = (type: StepType) => {
    const next = newStepFrom(presetFor(type))
    next.name = t(`editor.step.${type}`)
    setSteps((s) => [...s, next])
    setSelectedId(next.id)
  }

  const updateSelected = (patch: Partial<ADR0073Step>) => {
    if (!selected) return
    setSteps((s) => s.map((x) => (x.id === selected.id ? { ...x, ...patch } : x)))
  }

  const removeSelected = () => {
    if (!selected) return
    setSteps((s) => s.filter((x) => x.id !== selected.id))
    setSelectedId(null)
  }

  if (!isNew && defQ.isLoading) {
    return (
      <div className="flex justify-center py-12">
        <Spinner className="h-6 w-6" />
      </div>
    )
  }

  const canSave = name.trim().length > 0 && steps.length > 0 && issues.length === 0 && !save.isPending

  return (
    <div className="space-y-4">
      <PageHeader
        title={isNew ? t('editor.new_title') : t('editor.edit_title')}
        description={undefined}
        actions={
          <div className="flex items-center gap-2">
            <Button
              variant="ghost"
              size="sm"
              onClick={() => navigate({ to: '/workflows' })}
            >
              <ArrowLeft className="me-1 h-4 w-4" />
              {t('templates.title')}
            </Button>
            <Button onClick={() => save.mutate()} disabled={!canSave} data-testid="editor-save">
              {save.isPending ? (
                <Spinner className="me-1 h-3.5 w-3.5" />
              ) : (
                <Save className="me-1 h-3.5 w-3.5" />
              )}
              {save.isPending ? t('editor.saving') : t('editor.save')}
            </Button>
          </div>
        }
      />

      <div className="grid gap-4 lg:grid-cols-[18rem_minmax(0,1fr)_22rem]">
        {/* Left — template metadata + validation summary */}
        <Card className="space-y-3 p-4">
          <div className="space-y-1">
            <label className="block text-xs font-medium text-muted-foreground">
              {t('editor.name_label')}
            </label>
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={t('editor.name_placeholder')}
              data-testid="editor-name"
            />
          </div>
          <div className="space-y-1">
            <label className="block text-xs font-medium text-muted-foreground">
              {t('editor.description_label')}
            </label>
            <Textarea
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              rows={3}
              placeholder={t('editor.description_placeholder')}
              data-testid="editor-description"
            />
          </div>
          <div className="rounded-md border border-border bg-muted/30 p-2 text-xs">
            <p className="mb-1 font-medium text-foreground">{t('editor.validation_title')}</p>
            {issues.length === 0 && steps.length > 0 ? (
              <span className="inline-flex items-center gap-1 text-success">
                <CheckCircle2 className="h-3 w-3" />
                {t('editor.validation_ok')}
              </span>
            ) : (
              <span className="inline-flex items-center gap-1 text-warning">
                <AlertTriangle className="h-3 w-3" />
                {t('editor.validation_issue', {
                  count: issues.length,
                  defaultValue: issues.length === 1 ? `${issues.length} issue` : `${issues.length} issues`,
                })}
              </span>
            )}
          </div>
        </Card>

        {/* Center — chain + add-step menu */}
        <Card className="space-y-3 p-4">
          <div className="flex items-center justify-between">
            <h3 className="text-sm font-semibold">{t('editor.chain_title')}</h3>
            <StepPresetMenu onPick={addStep} />
          </div>
          <StepChainList
            steps={steps}
            selectedId={selectedId}
            onSelect={setSelectedId}
            onReorder={setSteps}
            issueIds={issueIds}
          />
        </Card>

        {/* Right — step config */}
        <Card className="p-4">
          <h3 className="mb-3 text-sm font-semibold">{t('editor.config_title')}</h3>
          {selected ? (
            <StepConfigPanel
              step={selected}
              onChange={updateSelected}
              onRemove={removeSelected}
              issues={issuesByStepId[selected.id] ?? []}
            />
          ) : (
            <p className="rounded-md border border-dashed border-border bg-muted/30 p-6 text-center text-sm text-muted-foreground">
              {t('editor.config_empty')}
            </p>
          )}
        </Card>
      </div>
    </div>
  )
}

// normalizeToFlatSteps walks the definition's steps and converts the
// legacy nested WorkflowStep shape (where on_true / on_false carry
// inline child steps) into the canonical flat ADR0073Step shape the
// editor + storage canon now use. Nested children get hoisted into
// the top-level chain with their id appended to the conditional
// step's on_true / on_false. Already-flat definitions pass through.
function normalizeToFlatSteps(def: WorkflowDefinition): ADR0073Step[] {
  const raw = def.steps as unknown as Array<Record<string, unknown>>
  const out: ADR0073Step[] = []
  const seen = new Set<string>()

  for (const s of raw) {
    const id = typeof s.id === 'string' && s.id ? s.id : `step-${out.length}`
    if (seen.has(id)) continue
    seen.add(id)
    const flat: ADR0073Step = {
      id,
      type: (s.type as ADR0073Step['type']) ?? 'approval',
      name: typeof s.name === 'string' ? s.name : '',
      assignee: parseAssignee(s),
      approvers: Array.isArray(s.approvers) ? (s.approvers as string[]) : undefined,
      mode: (s.mode as 'require_all' | 'require_any' | undefined) ?? undefined,
      sla_hours: typeof s.sla_hours === 'number' ? s.sla_hours : typeof s.timeout_hours === 'number' ? (s.timeout_hours as number) : undefined,
      on_expire: (s.on_expire as 'escalate' | 'auto_approve' | 'auto_reject' | undefined) ?? undefined,
      escalation: (s.escalation as ADR0073Step['escalation']) ?? undefined,
      condition_rego: typeof s.condition_rego === 'string' ? (s.condition_rego as string) : typeof s.condition === 'string' ? (s.condition as string) : undefined,
      on_true: Array.isArray(s.on_true) && s.on_true.length > 0 && typeof s.on_true[0] === 'string'
        ? (s.on_true as string[])
        : [],
      on_false: Array.isArray(s.on_false) && s.on_false.length > 0 && typeof s.on_false[0] === 'string'
        ? (s.on_false as string[])
        : [],
      allow_delegate: typeof s.allow_delegate === 'boolean' ? (s.allow_delegate as boolean) : undefined,
    }
    out.push(flat)
  }
  return out
}

function parseAssignee(s: Record<string, unknown>): ADR0073Step['assignee'] {
  if (s.assignee && typeof s.assignee === 'object') {
    const a = s.assignee as { type?: string; value?: string }
    if (a.value) return { type: (a.type as 'user' | 'group' | 'dynamic') ?? 'user', value: a.value }
  }
  if (typeof s.assignee_id === 'string' && s.assignee_id) {
    return { type: 'user', value: s.assignee_id }
  }
  if (typeof s.assignee_group === 'string' && s.assignee_group) {
    return { type: 'group', value: s.assignee_group }
  }
  return undefined
}

export const Route = createFileRoute('/_authenticated/workflows/$templateId/edit')({
  component: TemplateEditorPage,
})
