import { useState } from 'react'
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

const LEVEL_OPTS = CLASSIFICATION_LEVELS.map((l) => ({ value: l, label: l }))
const CLEARANCE_OPTS = [{ value: '', label: 'none' }, ...LEVEL_OPTS]
const ACTION_OPTS = RULE_ACTIONS.map((a) => ({ value: a, label: a === '*' ? 'any action' : a }))

export function ClassificationPage() {
  const qc = useQueryClient()
  const cfgQ = useQuery({ queryKey: ['admin', 'classification', 'config'], queryFn: getClassificationConfig })
  const rulesQ = useQuery({ queryKey: ['admin', 'classification', 'rules'], queryFn: listClassificationRules })

  const invalidate = (k: string) => qc.invalidateQueries({ queryKey: ['admin', 'classification', k] })

  return (
    <div className="mx-auto max-w-4xl p-6">
      <PageHeader
        title="Classification & access"
        description="Gate document view, download, and share by sensitivity level and per-user clearance."
      />

      <ExplainerBanner />

      {cfgQ.isLoading ? <Spinner /> : cfgQ.data && <ConfigCard cfg={cfgQ.data} onDone={() => invalidate('config')} />}

      <RuleBuilderCard onDone={() => invalidate('rules')} />

      {rulesQ.isLoading ? (
        <Spinner />
      ) : (
        <RulesTable rules={rulesQ.data ?? []} onDone={() => invalidate('rules')} />
      )}

      <ClearanceCard />
    </div>
  )
}

function ExplainerBanner() {
  return (
    <section className="mb-6 rounded-lg border border-blue-500/40 bg-blue-50/60 p-4 text-sm dark:bg-blue-950/20">
      <div className="flex items-start gap-3">
        <ShieldCheck className="mt-0.5 h-5 w-5 shrink-0 text-blue-600" />
        <div className="text-muted-foreground">
          <p className="font-semibold text-foreground">How it works</p>
          <p className="mt-1">
            Each document carries a sensitivity level (unclassified → internal → confidential →
            restricted), derived from the PII/PHI scan or set manually. A rule requires a minimum{' '}
            <em>clearance</em> to act on documents at or above a level. The PHI backstop blocks any
            PHI-flagged document for callers without <code>restricted</code> clearance, even with no
            explicit rule. The organization owner is always exempt (break-glass), and every block is
            written to the audit log.
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
  const [enabled, setEnabled] = useState(cfg.enabled)
  const [phiBackstop, setPhiBackstop] = useState(cfg.phi_requires_restricted)

  const save = useAppMutation({
    mutationFn: () => setClassificationConfig({ enabled, phi_requires_restricted: phiBackstop }),
    onSuccess: () => {
      toast.success('Enforcement settings saved')
      onDone()
    },
    onError: (e: unknown) => toast.error(`Save failed: ${(e as Error).message}`),
  })

  return (
    <Card className="mb-6 p-4">
      <div className="mb-3 text-sm font-semibold">Enforcement</div>
      <label className="flex items-center gap-2 text-sm">
        <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
        Enable classification-based access gating for this tenant
      </label>
      <label className="mt-2 flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={phiBackstop}
          onChange={(e) => setPhiBackstop(e.target.checked)}
        />
        PHI backstop — any PHI document requires <code className="mx-1">restricted</code> clearance
      </label>
      {enabled && (
        <p className="mt-3 flex items-center gap-1.5 text-xs text-amber-600">
          <AlertTriangle className="h-3.5 w-3.5" />
          When enabled, callers without sufficient clearance lose access to classified documents.
        </p>
      )}
      <div className="mt-4">
        <Button onClick={() => save.mutate()} disabled={save.isPending}>
          {save.isPending ? 'Saving…' : 'Save enforcement'}
        </Button>
      </div>
    </Card>
  )
}

function RuleBuilderCard({ onDone }: { onDone: () => void }) {
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
      toast.success('Rule added')
      setDescription('')
      onDone()
    },
    onError: (e: unknown) => toast.error(`Add failed: ${(e as Error).message}`),
  })

  return (
    <Card className="mb-6 p-4">
      <div className="mb-3 flex items-center gap-2 text-sm font-semibold">
        <Plus className="h-4 w-4" /> Add rule
      </div>
      <div className="grid gap-4 sm:grid-cols-3">
        <LabeledSelect
          label="Documents at/above"
          value={minClass}
          onValueChange={(v) => setMinClass(v as ClassificationLevel)}
          options={LEVEL_OPTS}
        />
        <LabeledSelect
          label="On action"
          value={action}
          onValueChange={(v) => setAction(v as RuleAction)}
          options={ACTION_OPTS}
        />
        <LabeledSelect
          label="Require clearance"
          value={required}
          onValueChange={(v) => setRequired(v as ClassificationLevel)}
          options={LEVEL_OPTS}
        />
      </div>
      <label className="mt-3 flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={appliesToPhi}
          onChange={(e) => setAppliesToPhi(e.target.checked)}
        />
        Also apply to any PHI-flagged document (regardless of its level)
      </label>
      <div className="mt-3">
        <Input
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          placeholder="Description (optional)"
        />
      </div>
      <div className="mt-4">
        <Button onClick={() => create.mutate()} disabled={create.isPending}>
          {create.isPending ? 'Adding…' : 'Add rule'}
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
  const del = useAppMutation({
    mutationFn: (id: string) => deleteClassificationRule(id),
    onSuccess: () => {
      toast.success('Rule removed')
      onDone()
    },
    onError: (e: unknown) => toast.error(`Remove failed: ${(e as Error).message}`),
  })

  return (
    <Card className="mb-6 p-4">
      <div className="mb-3 text-sm font-semibold">Rules</div>
      {rules.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          No rules. The identity default applies: a level-X document requires level-X clearance.
        </p>
      ) : (
        <div className="overflow-hidden rounded-md border border-border">
          <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="bg-muted/50 text-start text-xs uppercase text-muted-foreground">
              <tr>
                <th className="px-3 py-2">At/above</th>
                <th className="px-3 py-2">Action</th>
                <th className="px-3 py-2">Requires</th>
                <th className="px-3 py-2">PHI</th>
                <th className="px-3 py-2">Description</th>
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
                      <Trash2 className="h-3.5 w-3.5" /> Remove
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
  const [userID, setUserID] = useState('')
  const [clearance, setClearance] = useState<ClearanceLevel>('confidential')

  const save = useAppMutation({
    mutationFn: () => setUserClearance(userID.trim(), clearance),
    onSuccess: () => toast.success('Clearance updated'),
    onError: (e: unknown) => toast.error(`Update failed: ${(e as Error).message}`),
  })

  return (
    <Card className="p-4">
      <div className="mb-3 text-sm font-semibold">Assign clearance</div>
      <p className="mb-4 text-sm text-muted-foreground">
        Grant a user a maximum clearance level. Paste the user ID (from Identity &amp; Access).
      </p>
      <div className="grid gap-4 sm:grid-cols-2">
        <div>
          <label className="mb-1 block text-sm font-medium" htmlFor="clearance-user">
            User ID
          </label>
          <Input
            id="clearance-user"
            value={userID}
            onChange={(e) => setUserID(e.target.value)}
            placeholder="00000000-0000-0000-0000-000000000000"
          />
        </div>
        <LabeledSelect
          label="Clearance"
          value={clearance}
          onValueChange={(v) => setClearance(v as ClearanceLevel)}
          options={CLEARANCE_OPTS}
        />
      </div>
      <div className="mt-4">
        <Button onClick={() => save.mutate()} disabled={save.isPending || userID.trim() === ''}>
          {save.isPending ? 'Saving…' : 'Set clearance'}
        </Button>
      </div>
    </Card>
  )
}
