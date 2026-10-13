import { useTranslation } from 'react-i18next'
import { useState } from 'react'
import { createFileRoute, redirect } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
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
import { formatDateTime } from '@/lib/formatters'

const STATUS_VARIANT: Record<ModelStatus, string> = {
  training:   'in_review',
  evaluating: 'in_review',
  candidate:  'superseded',
  production: 'active',
  retired:    'archived',
  failed:     'disposed',
}

export function ModelsPage() {
  const { t } = useTranslation('common')
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

  const promote = useAppMutation({
    mutationFn: (id: string) => promoteModel(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['model-versions'] })
      toast.success(t('adm.mdl.promoted'))
    },
    onError: () => toast.error(t('adm.mdl.err_promote')),
  })

  const retire = useAppMutation({
    mutationFn: (id: string) => retireModel(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['model-versions'] })
      toast.success(t('adm.mdl.retired'))
    },
    onError: () => toast.error(t('adm.mdl.err_retire')),
  })

  const retrain = useAppMutation({
    mutationFn: () => triggerRetrain('classification'),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['model-versions'] })
      toast.success(t('adm.mdl.retrain_queued'))
    },
    onError: () => toast.error(t('adm.mdl.err_retrain')),
  })

  const rows = versions.data?.versions ?? []
  const production = rows.find((v) => v.status === 'production')

  // Gate "Trigger retrain" on the configured minimum example count. The
  // backend was queuing retrains even with 0 examples (a no-op/failed job),
  // so the disabled button is the user-facing guard that matches the empty-
  // state hint below. If either signal hasn't loaded — or the minimum is 0 —
  // we don't pre-block (the backend stays the final authority).
  const totalExamples = stats.data?.total ?? null
  const minExamples = config.data?.min_examples_for_retrain ?? null
  const insufficientExamples =
    totalExamples != null && minExamples != null && minExamples > 0 && totalExamples < minExamples

  return (
    <div>
      <PageHeader variant="section"
        title={t('adm.mdl.title')}
        description={t('adm.mdl.description')}
        actions={
          <Button
            size="sm"
            onClick={() => retrain.mutate()}
            disabled={retrain.isPending || insufficientExamples}
            title={
              insufficientExamples
                ? t('adm.mdl.retrain_blocked', { need: minExamples, have: (totalExamples ?? 0).toLocaleString() })
                : undefined
            }
          >
            <Play className="me-2 h-4 w-4" />
            {t('adm.mdl.retrain')}
          </Button>
        }
      />

      <div className="mt-6 grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Metric
          label={t('adm.mdl.m_production')}
          value={production ? production.version_tag : '—'}
          icon={<Brain className="h-4 w-4 text-success" />}
        />
        <Metric label={t('adm.mdl.m_examples')} value={(stats.data?.total ?? 0).toLocaleString()} />
        <Metric label={t('adm.mdl.m_unused')} value={(stats.data?.unused ?? 0).toLocaleString()} />
        <Metric
          label={t('adm.mdl.m_auto')}
          value={config.data?.auto_promote_if_better ? t('adm.mdl.on') : t('adm.mdl.off')}
        />
      </div>

      {/* Filter pill bar — seven pills in one row ran off the side of a
          phone and the last three could not be reached. */}
      <div className="mt-6 flex flex-wrap items-center gap-2 text-sm">
        <span className="text-muted-foreground">{t('adm.mdl.filter')}</span>
        {(['', 'production', 'candidate', 'training', 'evaluating', 'retired', 'failed'] as const).map(
          (s) => (
            <button
              key={s || 'all'}
              onClick={() => setFilterStatus(s)}
              className={`rounded px-2 py-1 text-xs ${
                filterStatus === s
                  ? 'bg-foreground text-background'
                  : 'bg-muted text-foreground hover:bg-muted/70'
              }`}
            >
              {s === '' ? t('adm.mdl.all') : t(`adm.mdl.s_${s}`, { defaultValue: s })}
            </button>
          ),
        )}
      </div>

      <div className="mt-4 rounded border border-border">
        <div className="border-b border-border bg-muted/40 px-4 py-2.5 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
          {t('adm.mdl.versions')}
        </div>
        <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead className="text-xs uppercase text-muted-foreground">
            <tr>
              <th className="px-4 py-2 text-start">{t('adm.mdl.col_version')}</th>
              <th className="px-4 py-2 text-start">{t('adm.mdl.col_type')}</th>
              <th className="px-4 py-2 text-start">{t('adm.mdl.col_status')}</th>
              <th className="px-4 py-2 text-end">{t('adm.mdl.col_examples')}</th>
              <th className="px-4 py-2 text-end">{t('adm.mdl.col_accuracy')}</th>
              <th className="px-4 py-2 text-start">{t('adm.mdl.col_trained')}</th>
              <th className="px-4 py-2 text-end"></th>
            </tr>
          </thead>
          <tbody>
            {versions.isLoading && (
              <tr><td colSpan={7} className="px-4 py-4 text-center text-muted-foreground">Loading…</td></tr>
            )}
            {!versions.isLoading && rows.length === 0 && (
              <tr><td colSpan={7} className="px-4 py-4 text-center text-muted-foreground">
                {t('adm.mdl.empty')}
                {(() => {
                  // Surface the threshold so the empty state reads as
                  // "you have N of M examples" instead of an alarming
                  // "nothing here" when retraining is correctly gated
                  // on a minimum example count. Falls back to the
                  // generic copy if either signal hasn't loaded.
                  const have = stats.data?.total ?? null
                  const need = config.data?.min_examples_for_retrain ?? null
                  if (have == null || need == null) {
                    return t('adm.mdl.empty_generic')
                  }
                  if (have < need) {
                    return t('adm.mdl.empty_need', { need, have: have.toLocaleString() })
                  }
                  return t('adm.mdl.empty_ready', { have: have.toLocaleString(), need: need.toLocaleString() })
                })()}
              </td></tr>
            )}
            {rows.map((v) => {
              const acc = readAccuracy(v.eval_metrics) ?? readAccuracy(v.training_metrics)
              return (
                <tr key={v.id} className="border-t border-border">
                  <td dir="ltr" className="px-4 py-2 font-mono">{v.version_tag}</td>
                  <td dir="ltr" className="px-4 py-2 text-muted-foreground">{v.model_type}</td>
                  <td className="px-4 py-2">
                    <Badge variant={STATUS_VARIANT[v.status]}>{t(`adm.mdl.s_${v.status}`, { defaultValue: v.status })}</Badge>
                    {v.error_message && (
                      <div className="mt-1 text-xs text-destructive">{v.error_message}</div>
                    )}
                  </td>
                  <td className="px-4 py-2 text-end tabular-nums">{v.training_examples_count}</td>
                  <td className="px-4 py-2 text-end tabular-nums">
                    {acc !== null ? t('adm.mdl.pct', { value: (acc * 100).toFixed(1) }) : '—'}
                  </td>
                  <td className="px-4 py-2 text-muted-foreground">{formatDateTime(v.created_at)}</td>
                  <td className="px-4 py-2 text-end">
                    <div className="flex justify-end gap-1">
                      {v.status === 'candidate' && (
                        <Button
                          size="sm"
                          variant="outline"
                          disabled={promote.isPending}
                          onClick={() => promote.mutate(v.id)}
                        >
                          <CheckCircle2 className="me-1 h-3 w-3" />
                          {t('adm.mdl.promote')}
                        </Button>
                      )}
                      {v.status !== 'retired' && v.status !== 'failed' && (
                        <Button
                          size="sm"
                          variant="ghost"
                          disabled={retire.isPending}
                          onClick={() => retire.mutate(v.id)}
                        >
                          <XCircle className="me-1 h-3 w-3" />
                          {t('adm.mdl.retire')}
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
      </div>

      {stats.data && stats.data.per_label.length > 0 && (
        <div className="mt-8 rounded border border-border">
          <div className="border-b border-border bg-muted/40 px-4 py-2.5 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
            {t('adm.mdl.by_label')}
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
  const { t } = useTranslation('common')
  const qc = useQueryClient()
  const [draft, setDraft] = useState(initial)
  const dirty = JSON.stringify(draft) !== JSON.stringify(initial)

  const save = useAppMutation({
    mutationFn: () => updateActiveLearningConfig(draft),
    onSuccess: (saved) => {
      qc.setQueryData(['active-learning-config'], saved)
      toast.success(t('adm.mdl.cfg_saved'))
    },
    onError: () => toast.error(t('adm.mdl.cfg_err_save')),
  })

  return (
    <div className="mt-8 rounded border border-border">
      <div className="border-b border-border bg-muted/40 px-4 py-2.5 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
        {t('adm.mdl.cfg_title')}
      </div>
      <div className="grid grid-cols-1 gap-4 p-4 text-sm sm:grid-cols-2">
        <Toggle
          label={t('adm.mdl.cfg_enabled')}
          help={t('adm.mdl.cfg_enabled_help')}
          checked={draft.enabled}
          onChange={(enabled) => setDraft({ ...draft, enabled })}
        />
        <Toggle
          label={t('adm.mdl.cfg_auto')}
          help={t('adm.mdl.cfg_auto_help')}
          checked={draft.auto_promote_if_better}
          onChange={(auto_promote_if_better) => setDraft({ ...draft, auto_promote_if_better })}
        />
        <NumField
          label={t('adm.mdl.cfg_min_examples')}
          value={draft.min_examples_for_retrain}
          step={5}
          onChange={(min_examples_for_retrain) => setDraft({ ...draft, min_examples_for_retrain })}
        />
        <NumField
          label={t('adm.mdl.cfg_increment')}
          value={draft.retrain_increment}
          step={5}
          onChange={(retrain_increment) => setDraft({ ...draft, retrain_increment })}
        />
        <NumField
          label={t('adm.mdl.cfg_min_accuracy')}
          value={draft.min_accuracy_improvement}
          step={0.01}
          onChange={(min_accuracy_improvement) =>
            setDraft({ ...draft, min_accuracy_improvement })
          }
        />
        <NumField
          label={t('adm.mdl.cfg_validation')}
          value={draft.train_validation_split}
          step={0.05}
          onChange={(train_validation_split) => setDraft({ ...draft, train_validation_split })}
        />
        <NumField
          label={t('adm.mdl.cfg_test')}
          value={draft.train_test_split}
          step={0.05}
          onChange={(train_test_split) => setDraft({ ...draft, train_test_split })}
        />
      </div>
      <div className="flex justify-end border-t border-border px-4 py-3">
        <Button size="sm" disabled={!dirty || save.isPending} onClick={() => save.mutate()}>
          {t('adm.mdl.cfg_save')}
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

// Merged surface — this standalone URL redirects into the canonical
// tabbed page (/admin/ai?tab=models). The page component stays
// exported so the shell can embed it: one rendering, one URL.
export const Route = createFileRoute('/_authenticated/admin/intelligence/models')({
  beforeLoad: () => {
    throw redirect({ to: '/admin/ai', search: { tab: 'models' }, replace: true })
  },
})
