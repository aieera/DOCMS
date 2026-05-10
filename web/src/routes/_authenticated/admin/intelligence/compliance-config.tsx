import { useEffect, useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { Save } from 'lucide-react'

import {
  getComplianceConfig,
  updateComplianceConfig,
  type ComplianceConfig,
} from '@/api/compliance-pii'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/Input'

const RISK_LEVELS = ['critical', 'high', 'medium', 'low'] as const

function ComplianceConfigPage() {
  const qc = useQueryClient()
  const { data, isLoading } = useQuery({
    queryKey: ['compliance-config'],
    queryFn: getComplianceConfig,
  })
  const [draft, setDraft] = useState<ComplianceConfig | null>(null)
  const [overridesText, setOverridesText] = useState('{}')
  const [patternsText, setPatternsText] = useState('[]')
  const [rolesInput, setRolesInput] = useState('')

  useEffect(() => {
    if (data && !draft) {
      setDraft(data)
      setOverridesText(JSON.stringify(data.pii_entity_risk_overrides ?? {}, null, 2))
      setPatternsText(JSON.stringify(data.custom_patterns ?? [], null, 2))
      setRolesInput((data.notify_roles ?? []).join(', '))
    }
  }, [data, draft])

  const save = useMutation({
    mutationFn: (patch: Partial<ComplianceConfig>) => updateComplianceConfig(patch),
    onSuccess: (next) => {
      qc.setQueryData(['compliance-config'], next)
      setDraft(next)
      toast.success('Compliance config saved')
    },
    onError: () => toast.error('Save failed'),
  })

  if (isLoading || !draft) return <div className="p-6 text-sm text-muted-foreground">Loading…</div>

  const onSave = () => {
    let overrides: Record<string, string>
    try {
      overrides = JSON.parse(overridesText)
    } catch {
      toast.error('Risk overrides: invalid JSON')
      return
    }
    for (const [k, v] of Object.entries(overrides)) {
      if (!RISK_LEVELS.includes(v as never)) {
        toast.error(`Risk for ${k} must be one of ${RISK_LEVELS.join('/')}`)
        return
      }
    }
    let patterns: { type: string; regex: string; risk?: string }[]
    try {
      patterns = JSON.parse(patternsText)
      if (!Array.isArray(patterns)) throw new Error('not array')
    } catch {
      toast.error('Custom patterns: invalid JSON array')
      return
    }
    if (patterns.length > 32) {
      toast.error('Max 32 custom patterns')
      return
    }
    const roles = rolesInput.split(',').map((s) => s.trim()).filter(Boolean)
    if (roles.length === 0) {
      toast.error('At least one notify role required')
      return
    }
    save.mutate({
      ...draft,
      notify_roles: roles,
      pii_entity_risk_overrides: overrides,
      custom_patterns: patterns,
    })
  }

  return (
    <div className="mx-auto max-w-3xl p-6">
      <PageHeader
        title="Compliance config"
        description="Per-tenant scanning thresholds, notification routing, PHI opt-in, and custom regex patterns (ADR 0054)."
      />

      <div className="mt-6 space-y-6 rounded border border-border p-5">
        <Toggle
          label="Enable compliance scanning"
          checked={draft.enabled}
          onChange={(v) => setDraft({ ...draft, enabled: v })}
        />
        <Toggle
          label="Auto-flag for legal hold on critical findings"
          help="Marks the document for hold review only — actual hold is placed manually via the legal-hold UI (ADR 0054 §auto-hold)."
          checked={draft.auto_hold_on_critical}
          onChange={(v) => setDraft({ ...draft, auto_hold_on_critical: v })}
        />
        <Toggle
          label="Notify on high+ findings"
          checked={draft.notify_on_high}
          onChange={(v) => setDraft({ ...draft, notify_on_high: v })}
        />
        <Toggle
          label="Enable PHI scanning (HIPAA opt-in)"
          help="Off by default. Detects medical record numbers, diagnoses, lab results, etc."
          checked={draft.phi_enabled}
          onChange={(v) => setDraft({ ...draft, phi_enabled: v })}
        />

        <div>
          <label className="block text-sm font-medium">Notification roles</label>
          <p className="mt-1 text-xs text-muted-foreground">
            Comma-separated. Receivers of `dms.notification.send.v1` events.
          </p>
          <Input
            value={rolesInput}
            onChange={(e) => setRolesInput(e.target.value)}
            placeholder="compliance_officer, admin"
            className="mt-2"
          />
        </div>

        <div>
          <label className="block text-sm font-medium">Risk-level overrides</label>
          <p className="mt-1 text-xs text-muted-foreground">
            JSON object: {`{"EMAIL":"high","SSN":"critical" }`}.
            Each value must be critical/high/medium/low.
          </p>
          <textarea
            value={overridesText}
            onChange={(e) => setOverridesText(e.target.value)}
            rows={5}
            className="mt-2 w-full rounded border border-border bg-muted/40 p-2 font-mono text-xs"
          />
        </div>

        <div>
          <label className="block text-sm font-medium">Custom regex patterns</label>
          <p className="mt-1 text-xs text-muted-foreground">
            JSON array, max 32. Each entry: {`{"type":"TICKET_ID","regex":"TICK-\\d+" }`}.
          </p>
          <textarea
            value={patternsText}
            onChange={(e) => setPatternsText(e.target.value)}
            rows={6}
            className="mt-2 w-full rounded border border-border bg-muted/40 p-2 font-mono text-xs"
          />
        </div>

        <div className="flex justify-end">
          <Button onClick={onSave} disabled={save.isPending}>
            <Save className="mr-2 h-4 w-4" />
            Save
          </Button>
        </div>
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
    <div className="flex items-start justify-between gap-4 text-sm">
      <div>
        <div>{label}</div>
        {help && <p className="mt-1 text-xs text-muted-foreground">{help}</p>}
      </div>
      <input
        type="checkbox"
        className="mt-1 h-4 w-4"
        checked={checked}
        onChange={(e) => onChange(e.target.checked)}
      />
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/intelligence/compliance-config')({
  component: ComplianceConfigPage,
})
