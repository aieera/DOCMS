import { useState } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { createFileRoute, redirect } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ShieldCheck, Trash2, Plus, AlertTriangle } from 'lucide-react'
import { toast } from 'sonner'

import { useAppMutation } from '@/hooks/useAppMutation'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { LabeledSelect } from '@/components/ui/shadcn/select'
import { Card } from '@/components/ui/card'
import { Spinner } from '@/components/ui/Spinner'
import {
  getClassificationConfig,
  setClassificationConfig,
  listClassificationRules,
  createClassificationRule,
  deleteClassificationRule,
  setUserClearance,
  CLASSIFICATION_LEVELS,
  RULE_ACTIONS,
  type ClassificationLevel,
  type ClearanceLevel,
  type RuleAction,
} from '@/api/classification'

// /admin/tenant/classification — classification-based access control (§8).
// Define classification→access rules, toggle enforcement, and assign per-user
// clearance. Enforcement is off until enabled, so turning it on is deliberate.

// Merged surface — this standalone URL redirects into the canonical
// tabbed page (/admin/protection?tab=classification). The page component stays
// exported so the shell can embed it: one rendering, one URL.
export const Route = createFileRoute('/_authenticated/admin/tenant/classification')({
  beforeLoad: () => {
    throw redirect({ to: '/admin/protection', search: { tab: 'classification' }, replace: true })
  },
})

// Built inside the component, not at module scope: the labels come
// from the catalogue and must follow a locale switch.
const LEVEL_VALUES = CLASSIFICATION_LEVELS
const ACTION_VALUES = RULE_ACTIONS

export function ClassificationPage() {
  const { t } = useTranslation('common')
  const qc = useQueryClient()
  const cfgQ = useQuery({ queryKey: ['admin', 'classification', 'config'], queryFn: getClassificationConfig })
  const rulesQ = useQuery({ queryKey: ['admin', 'classification', 'rules'], queryFn: listClassificationRules })

  const invalidate = (k: string) => qc.invalidateQueries({ queryKey: ['admin', 'classification', k] })

  return (
    // No mx-auto/max-w cap and no padding of its own: the tab panel is
    // already 1596px wide inside app-layout's own p-4/sm:p-6/lg:p-8, so
    // `mx-auto max-w-4xl p-6` pinned 896px of content in the middle and
    // left ~350px dead on each side -- with the tab bar still hard against
    // the start edge, so the tabs and their own panel did not line up.
    <div className="space-y-6">
      <PageHeader
        noMargin
        title={t('protection.cls.title')}
        description={t('protection.cls.description')}
      />

      <ExplainerBanner />

      {/* Tenant-wide switches on the left, rule authoring on the right.
          The rule builder and the rules it produces belong together in
          one column: you add a rule and watch it appear directly below. */}
      <div className="grid gap-6 xl:grid-cols-[minmax(320px,400px)_minmax(0,1fr)]">
        <div className="space-y-6">
          {cfgQ.isLoading ? (
            <Spinner />
          ) : (
            cfgQ.data && <ConfigCard cfg={cfgQ.data} onDone={() => invalidate('config')} />
          )}
          <ClearanceCard />
        </div>

        <div className="min-w-0 space-y-6">
          <RuleBuilderCard onDone={() => invalidate('rules')} />
          {rulesQ.isLoading ? (
            <Spinner />
          ) : (
            <RulesTable rules={rulesQ.data ?? []} onDone={() => invalidate('rules')} />
          )}
        </div>
      </div>
    </div>
  )
}

function ExplainerBanner() {
  const { t } = useTranslation('common')
  return (
    <section className="rounded-lg border border-blue-500/40 bg-blue-50/60 p-4 text-sm dark:bg-blue-950/20">
      <div className="flex items-start gap-3">
        <ShieldCheck className="mt-0.5 h-5 w-5 shrink-0 text-blue-600" />
        {/* max-w-[85ch]: the banner spans the panel, but its prose must
            not. At 1596px this ran ~200 characters to the line, which is
            about three times a readable measure. */}
        <div className="max-w-[85ch] text-muted-foreground">
          <p className="font-semibold text-foreground">{t('protection.how_it_works')}</p>
          {/* Trans: the emphasised term and the <code> level sit inside
              the sentence, and Arabic reorders the clauses around them. */}
          <p className="mt-1">
            <Trans
              i18nKey="protection.cls.explainer"
              ns="common"
              components={{ 1: <em />, 2: <code dir="ltr" /> }}
            />
          </p>
        </div>
      </div>
    </section>
  )
}

function ConfigCard({
  cfg,
  onDone,
}: {
  cfg: { enabled: boolean; phi_requires_restricted: boolean }
  onDone: () => void
}) {
  const { t } = useTranslation('common')
  const [enabled, setEnabled] = useState(cfg.enabled)
  const [phiBackstop, setPhiBackstop] = useState(cfg.phi_requires_restricted)

  const save = useAppMutation({
    mutationFn: () => setClassificationConfig({ enabled, phi_requires_restricted: phiBackstop }),
    onSuccess: () => {
      toast.success(t('protection.cls.saved'))
      onDone()
    },
    onError: (e: unknown) => toast.error(`Save failed: ${(e as Error).message}`),
  })

  return (
    <Card className="p-4">
      <div className="mb-3 text-sm font-semibold">{t('protection.cls.enforcement')}</div>
      {/* items-start + the label text in ONE span. As bare children of a
          flex row each text run and the <code> were separate flex items,
          so in a narrow column they wrapped as boxes with gap-2 between
          them -- "…requires        restricted   clearance" -- instead of
          reflowing as a sentence. */}
      <label className="flex items-start gap-2 text-sm">
        <input
          type="checkbox"
          className="mt-0.5 shrink-0"
          checked={enabled}
          onChange={(e) => setEnabled(e.target.checked)}
        />
        <span>{t('protection.cls.enable')}</span>
      </label>
      <label className="mt-2 flex items-start gap-2 text-sm">
        <input
          type="checkbox"
          className="mt-0.5 shrink-0"
          checked={phiBackstop}
          onChange={(e) => setPhiBackstop(e.target.checked)}
        />
        <span>
          {/* The level identifier sits mid-sentence and the word order
              around it differs in Arabic, so it travels with the string. */}
          <Trans
            i18nKey="protection.cls.phi_backstop_full"
            ns="common"
            components={{ 1: <code className="mx-1" dir="ltr" /> }}
          />
        </span>
      </label>
      {enabled && (
        <p className="mt-3 flex items-center gap-1.5 text-xs text-warning-strong">
          <AlertTriangle className="h-3.5 w-3.5" />
          {t('protection.cls.enforce_note')}
        </p>
      )}
      <div className="mt-4">
        <Button onClick={() => save.mutate()} disabled={save.isPending}>
          {save.isPending ? t('protection.cls.saving') : t('protection.cls.save_enforcement')}
        </Button>
      </div>
    </Card>
  )
}

function RuleBuilderCard({ onDone }: { onDone: () => void }) {
  const { t } = useTranslation('common')
  const levelOpts = LEVEL_VALUES.map((l) => ({ value: l, label: t(`protection.level.${l}`) }))
  const actionOpts = ACTION_VALUES.map((a) => ({ value: a, label: t(`protection.action.${a}`) }))
  const [minClass, setMinClass] = useState<ClassificationLevel>('confidential')
  const [action, setAction] = useState<RuleAction>('*')
  const [required, setRequired] = useState<ClassificationLevel>('confidential')
  const [appliesToPhi, setAppliesToPhi] = useState(false)
  const [description, setDescription] = useState('')

  const create = useAppMutation({
    mutationFn: () =>
      createClassificationRule({
        min_classification: minClass,
        action,
        required_clearance: required,
        applies_to_phi: appliesToPhi,
        description,
      }),
    onSuccess: () => {
      toast.success(t('protection.cls.rule_added'))
      setDescription('')
      onDone()
    },
    onError: (e: unknown) => toast.error(`Add failed: ${(e as Error).message}`),
  })

  return (
    <Card className="p-4">
      <div className="mb-3 flex items-center gap-2 text-sm font-semibold">
        <Plus className="h-4 w-4" /> {t('protection.cls.add_rule')}
      </div>
      <div className="grid gap-4 sm:grid-cols-3">
        <LabeledSelect
          label={t('protection.cls.at_above')}
          value={minClass}
          onValueChange={(v) => setMinClass(v as ClassificationLevel)}
          options={levelOpts}
        />
        <LabeledSelect
          label={t('protection.cls.on_action')}
          value={action}
          onValueChange={(v) => setAction(v as RuleAction)}
          options={actionOpts}
        />
        <LabeledSelect
          label={t('protection.cls.require_clearance')}
          value={required}
          onValueChange={(v) => setRequired(v as ClassificationLevel)}
          options={levelOpts}
        />
      </div>
      <label className="mt-3 flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={appliesToPhi}
          onChange={(e) => setAppliesToPhi(e.target.checked)}
        />
        {t('protection.cls.also_phi')}
      </label>
      <div className="mt-3">
        <Input
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          placeholder={t('protection.cls.desc_placeholder')}
        />
      </div>
      <div className="mt-4">
        <Button onClick={() => create.mutate()} disabled={create.isPending}>
          {create.isPending ? t('protection.cls.adding') : t('protection.cls.add_rule')}
        </Button>
      </div>
    </Card>
  )
}

function RulesTable({
  rules,
  onDone,
}: {
  rules: import('@/api/classification').ClassificationRule[]
  onDone: () => void
}) {
  const { t } = useTranslation('common')
  const del = useAppMutation({
    mutationFn: (id: string) => deleteClassificationRule(id),
    onSuccess: () => {
      toast.success(t('protection.cls.rule_removed'))
      onDone()
    },
    onError: (e: unknown) => toast.error(`Remove failed: ${(e as Error).message}`),
  })

  return (
    <Card className="p-4">
      <div className="mb-3 text-sm font-semibold">{t('protection.cls.rules')}</div>
      {rules.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          {t('protection.cls.no_rules')}
        </p>
      ) : (
        <div className="overflow-hidden rounded-md border border-border">
          <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="bg-muted/50 text-start text-xs uppercase text-muted-foreground">
              <tr>
                <th className="px-3 py-2">{t('protection.cls.col_at_above')}</th>
                <th className="px-3 py-2">{t('protection.cls.col_action')}</th>
                <th className="px-3 py-2">{t('protection.cls.col_requires')}</th>
                <th className="px-3 py-2">{t('protection.cls.col_phi')}</th>
                <th className="px-3 py-2">{t('protection.cls.col_description')}</th>
                <th className="px-3 py-2" />
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {rules.map((r) => (
                <tr key={r.id}>
                  <td className="px-3 py-2 font-mono">{r.min_classification}</td>
                  <td className="px-3 py-2">{r.action === '*' ? 'any' : r.action}</td>
                  <td className="px-3 py-2 font-mono">{r.required_clearance}</td>
                  <td className="px-3 py-2">{r.applies_to_phi ? 'yes' : '—'}</td>
                  <td className="px-3 py-2 text-muted-foreground">{r.description || '—'}</td>
                  <td className="px-3 py-2 text-end">
                    <Button
                      variant="ghost"
                      size="sm"
                      className="gap-1 text-red-600 hover:text-red-700"
                      onClick={() => del.mutate(r.id)}
                      disabled={del.isPending}
                    >
                      <Trash2 className="h-3.5 w-3.5" /> {t('protection.cls.remove')}
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          </div>
        </div>
      )}
    </Card>
  )
}

function ClearanceCard() {
  const { t } = useTranslation('common')
  const clearanceOpts = [{ value: '', label: t('protection.cls.clearance_none') },
    ...CLASSIFICATION_LEVELS.map((l) => ({ value: l, label: t(`protection.level.${l}`) }))]
  const [userID, setUserID] = useState('')
  const [clearance, setClearance] = useState<ClearanceLevel>('confidential')

  const save = useAppMutation({
    mutationFn: () => setUserClearance(userID.trim(), clearance),
    onSuccess: () => toast.success(t('protection.cls.clearance_updated')),
    onError: (e: unknown) => toast.error(`Update failed: ${(e as Error).message}`),
  })

  return (
    <Card className="p-4">
      <div className="mb-3 text-sm font-semibold">{t('protection.cls.assign')}</div>
      <p className="mb-4 text-sm text-muted-foreground">
        {t('protection.cls.assign_desc')}
      </p>
      <div className="grid gap-4 sm:grid-cols-2">
        <div>
          <label className="mb-1 block text-sm font-medium" htmlFor="clearance-user">
            {t('protection.cls.user_id')}
          </label>
          <Input
            id="clearance-user"
            value={userID}
            onChange={(e) => setUserID(e.target.value)}
            placeholder={t('protection.cls.uuid_placeholder')}
          />
        </div>
        <LabeledSelect
          label={t('protection.cls.clearance')}
          value={clearance}
          onValueChange={(v) => setClearance(v as ClearanceLevel)}
          options={clearanceOpts}
        />
      </div>
      <div className="mt-4">
        <Button onClick={() => save.mutate()} disabled={save.isPending || userID.trim() === ''}>
          {save.isPending ? t('protection.cls.saving') : t('protection.cls.set_clearance')}
        </Button>
      </div>
    </Card>
  )
}
