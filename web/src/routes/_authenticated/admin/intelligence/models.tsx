import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { Brain, CheckCircle2, Play, XCircle } from 'lucide-react'

import {
  getActiveLearningConfig,
  getTrainingExampleStats,
  listModelVersions,
  promoteModel,
  retireModel,
  triggerRetrain,
  updateActiveLearningConfig,
  type ActiveLearningConfig,
  type ModelStatus,
  type ModelVersion,
} from '@/api/models'
import { PageHeader } from '@/components/shared/PageHeader'
import { Badge } from '@/components/ui/shadcn/badge'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'

const STATUS_VARIANT: Record<ModelStatus, string> = {
  training:   'in_review',
  evaluating: 'in_review',
  candidate:  'superseded',
  production: 'active',
  retired:    'archived',
  failed:     'disposed',
}

function ModelsPage() {
  const qc = useQueryClient()
  const [filterStatus, setFilterStatus] = useState<ModelStatus | ''>('')

  const versions = useQuery({
    queryKey: ['model-versions', filterStatus],
    queryFn: () => listModelVersions({ status: filterStatus || undefined, limit: 50 }),
    refetchInterval: (q) => {
      const rows = (q.state.data as { versions: ModelVersion[] } | undefined)?.versions ?? []
      return rows.some((v) => v.status === 'training' || v.status === 'evaluating')
        ? 5_000
        : 30_000
    },
  })
  const stats = useQuery({
    queryKey: ['training-example-stats'],
    queryFn: getTrainingExampleStats,
    refetchInterval: 30_000,
  })
  const config = useQuery({
    queryKey: ['active-learning-config'],
    queryFn: getActiveLearningConfig,
  })

  const promote = useMutation({
    mutationFn: (id: string) => promoteModel(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['model-versions'] })
      toast.success('Model promoted to production')
    },
    onError: () => toast.error('Promote failed'),
  })

  const retire = useMutation({
    mutationFn: (id: string) => retireModel(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['model-versions'] })
      toast.success('Model retired')
    },
    onError: () => toast.error('Retire failed'),
  })

  const retrain = useMutation({
    mutationFn: () => triggerRetrain('classification'),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['model-versions'] })
      toast.success('Retrain queued')
    },
    onError: () => toast.error('Failed to queue retrain'),
  })

  const rows = versions.data?.versions ?? []
  const production = rows.find((v) => v.status === 'production')

  return (
    <div className="mx-auto max-w-6xl p-6">
      <PageHeader
        title="Model registry"
        description="Per-tenant fine-tuned classifiers (ADR 0060). New versions are trained from your manual corrections."
        actions={
          <Button size="sm" onClick={() => retrain.mutate()} disabled={retrain.isPending}>
            <Play className="mr-2 h-4 w-4" />
            Trigger retrain
          </Button>
        }
      />

      <div className="mt-6 grid grid-cols-4 gap-4">
        <Metric
          label="Production"
          value={production ? production.version_tag : '—'}
          icon={<Brain className="h-4 w-4 text-success" />}
        />
        <Metric label="Examples" value={(stats.data?.total ?? 0).toLocaleString()} />
        <Metric label="Unused" value={(stats.data?.unused ?? 0).toLocaleString()} />
        <Metric
          label="Auto-promote"
          value={config.data?.auto_promote_if_better ? 'On' : 'Off'}
        />
      </div>

      {/* Filter pill bar */}
      <div className="mt-6 flex items-center gap-2 text-sm">
        <span className="text-muted-foreground">Filter:</span>
        {(['', 'production', 'candidate', 'training', 'evaluating', 'retired', 'failed'] as const).map(
          (s) => (
            <button
              key={s || 'all'}
              onClick={() => setFilterStatus(s)}
              className={`rounded px-2 py-1 text-xs ${
                filterStatus === s
                  ? 'bg-foreground text-background'
                  : 'bg-muted text-foreground hover:bg-muted  '
              }`}
            >
              {s === '' ? 'All' : s}
            </button>
          ),
        )}
      </div>

      <div className="mt-4 rounded border border-border">
        <div className="border-b border-border bg-muted/40 px-4 py-2.5 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
          Model versions
        </div>
        <table className="w-full text-sm">
          <thead className="text-left text-xs uppercase text-muted-foreground">
            <tr>
              <th className="px-4 py-2">Version</th>
              <th className="px-4 py-2">Type</th>
              <th className="px-4 py-2">Status</th>
              <th className="px-4 py-2 text-right">Examples</th>
              <th className="px-4 py-2 text-right">Accuracy</th>
              <th className="px-4 py-2">Trained</th>
              <th className="px-4 py-2 text-right"></th>
            </tr>
          </thead>
          <tbody>
            {versions.isLoading && (
              <tr><td colSpan={7} className="px-4 py-4 text-center text-muted-foreground">Loading…</td></tr>
            )}
            {!versions.isLoading && rows.length === 0 && (
              <tr><td colSpan={7} className="px-4 py-4 text-center text-muted-foreground">
                No model versions yet. Trigger a retrain once you have training examples.
              </td></tr>
            )}
            {rows.map((v) => {
              const acc = readAccuracy(v.eval_metrics) ?? readAccuracy(v.training_metrics)
              return (
                <tr key={v.id} className="border-t border-border">
                  <td className="px-4 py-2 font-mono">{v.version_tag}</td>
                  <td className="px-4 py-2 text-muted-foreground">{v.model_type}</td>
                  <td className="px-4 py-2">
                    <Badge variant={STATUS_VARIANT[v.status]}>{v.status}</Badge>
                    {v.error_message && (
                      <div className="mt-1 text-xs text-destructive">{v.error_message}</div>
                    )}
                  </td>
                  <td className="px-4 py-2 text-right tabular-nums">{v.training_examples_count}</td>
                  <td className="px-4 py-2 text-right tabular-nums">
                    {acc !== null ? `${(acc * 100).toFixed(1)}%` : '—'}
                  </td>
                  <td className="px-4 py-2 text-muted-foreground">{new Date(v.created_at).toLocaleString()}</td>
                  <td className="px-4 py-2 text-right">
                    <div className="flex justify-end gap-1">
                      {v.status === 'candidate' && (
                        <Button
                          size="sm"
                          variant="outline"
                          disabled={promote.isPending}
                          onClick={() => promote.mutate(v.id)}
                        >
                          <CheckCircle2 className="mr-1 h-3 w-3" />
                          Promote
                        </Button>
                      )}
                      {v.status !== 'retired' && v.status !== 'failed' && (
                        <Button
                          size="sm"
                          variant="ghost"
                          disabled={retire.isPending}
                          onClick={() => retire.mutate(v.id)}
                        >
                          <XCircle className="mr-1 h-3 w-3" />
                          Retire
                        </Button>
                      )}
                    </div>
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>

      {stats.data && stats.data.per_label.length > 0 && (
        <div className="mt-8 rounded border border-border">
          <div className="border-b border-border bg-muted/40 px-4 py-2.5 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
            Training examples by label
          </div>
          <ul className="divide-y divide-zinc-100 text-sm dark:divide-zinc-900">
            {stats.data.per_label.map((l) => (
              <li key={l.label} className="flex items-center justify-between px-4 py-2">
                <span>{l.label}</span>
                <span className="tabular-nums text-muted-foreground">{l.count}</span>
              </li>
            ))}
          </ul>
        </div>
      )}

      {config.data && <ConfigPanel initial={config.data} />}
    </div>
  )
}

function readAccuracy(m: unknown): number | null {
  if (!m || typeof m !== 'object') return null
  const v = (m as Record<string, unknown>).accuracy
  return typeof v === 'number' ? v : null
}

function ConfigPanel({ initial }: { initial: ActiveLearningConfig }) {
  const qc = useQueryClient()
  const [draft, setDraft] = useState(initial)
  const dirty = JSON.stringify(draft) !== JSON.stringify(initial)

  const save = useMutation({
    mutationFn: () => updateActiveLearningConfig(draft),
    onSuccess: (saved) => {
      qc.setQueryData(['active-learning-config'], saved)
      toast.success('Config saved')
    },
    onError: () => toast.error('Save failed'),
  })

  return (
    <div className="mt-8 rounded border border-border">
      <div className="border-b border-border bg-muted/40 px-4 py-2.5 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
        Active-learning configuration
      </div>
      <div className="grid grid-cols-2 gap-4 p-4 text-sm">
        <Toggle
          label="Enabled"
          help="Collect corrections and trigger retraining."
          checked={draft.enabled}
          onChange={(enabled) => setDraft({ ...draft, enabled })}
        />
        <Toggle
          label="Auto-promote when better"
          help="Skip manual review when the candidate beats production by the threshold below."
          checked={draft.auto_promote_if_better}
          onChange={(auto_promote_if_better) => setDraft({ ...draft, auto_promote_if_better })}
        />
        <NumField
          label="Min examples before first retrain"
          value={draft.min_examples_for_retrain}
          step={5}
          onChange={(min_examples_for_retrain) => setDraft({ ...draft, min_examples_for_retrain })}
        />
        <NumField
          label="Retrain every N new examples"
          value={draft.retrain_increment}
          step={5}
          onChange={(retrain_increment) => setDraft({ ...draft, retrain_increment })}
        />
        <NumField
          label="Min accuracy improvement"
          value={draft.min_accuracy_improvement}
          step={0.01}
          onChange={(min_accuracy_improvement) =>
            setDraft({ ...draft, min_accuracy_improvement })
          }
        />
        <NumField
          label="Validation split"
          value={draft.train_validation_split}
          step={0.05}
          onChange={(train_validation_split) => setDraft({ ...draft, train_validation_split })}
        />
        <NumField
          label="Test split"
          value={draft.train_test_split}
          step={0.05}
          onChange={(train_test_split) => setDraft({ ...draft, train_test_split })}
        />
      </div>
      <div className="flex justify-end border-t border-border px-4 py-3">
        <Button size="sm" disabled={!dirty || save.isPending} onClick={() => save.mutate()}>
          Save
        </Button>
      </div>
    </div>
  )
}

function Toggle({
  label,
  help,
  checked,
  onChange,
}: {
  label: string
  help?: string
  checked: boolean
  onChange: (v: boolean) => void
}) {
  return (
    <label className="flex cursor-pointer items-start gap-3">
      <input
        type="checkbox"
        checked={checked}
        onChange={(e) => onChange(e.target.checked)}
        className="mt-1"
      />
      <div>
        <div className="font-medium">{label}</div>
        {help && <div className="text-xs text-muted-foreground">{help}</div>}
      </div>
    </label>
  )
}

function NumField({
  label,
  value,
  step,
  onChange,
}: {
  label: string
  value: number
  step: number
  onChange: (v: number) => void
}) {
  return (
    <label className="flex flex-col gap-1">
      <span className="text-xs uppercase tracking-wide text-muted-foreground">{label}</span>
      <Input
        type="number"
        step={step}
        value={value}
        onChange={(e) => {
          const n = Number(e.target.value)
          if (!Number.isNaN(n)) onChange(n)
        }}
      />
    </label>
  )
}

function Metric({
  label,
  value,
  icon,
}: {
  label: string
  value: string
  icon?: React.ReactNode
}) {
  return (
    <div className="rounded border border-border p-4">
      <div className="flex items-center gap-2 text-xs uppercase tracking-wide text-muted-foreground">
        {icon}
        {label}
      </div>
      <div className="mt-1 text-2xl font-semibold tabular-nums">{value}</div>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/intelligence/models')({
  component: ModelsPage,
})
