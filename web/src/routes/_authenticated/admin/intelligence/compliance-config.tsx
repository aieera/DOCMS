import { useEffect, useState } from 'react'
import { createFileRoute, redirect } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { Save } from 'lucide-react'

import {
  getComplianceConfig,
  updateComplianceConfig,
  type ComplianceConfig,
} from '@/api/compliance-pii'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { ErrorState } from '@/components/ui/ErrorState'

const RISK_LEVELS = ['critical', 'high', 'medium', 'low'] as const

// Returns an error string for the current risk-overrides text, or null. Pure
// + recomputed each render so the message can never go stale relative to the
// field (the old toast-only flow kept showing "Risk for EMAIL…" after the
// user had already corrected the override back to {}).
function validateOverrides(text: string): string | null {
  let parsed: unknown
  try {
    parsed = JSON.parse(text)
  } catch {
    return 'invalid JSON'
  }
  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
    return 'must be a JSON object'
  }
  for (const [k, v] of Object.entries(parsed)) {
    if (typeof v !== 'string' || !RISK_LEVELS.includes(v as never)) {
      return `Risk for ${k} must be one of ${RISK_LEVELS.join('/')}`
    }
  }
  return null
}

// Validates the custom-patterns JSON AND that each entry's regex actually
// compiles — an unclosed group/class like "(unclosed[" is valid JSON but an
// invalid RegExp that would blow up the scanner at runtime, and was
// previously saved without any check.
function validatePatterns(text: string): string | null {
  let parsed: unknown
  try {
    parsed = JSON.parse(text)
  } catch {
    return 'invalid JSON array'
  }
  if (!Array.isArray(parsed)) return 'must be a JSON array'
  if (parsed.length > 32) return 'max 32 custom patterns'
  for (const [i, p] of parsed.entries()) {
    if (typeof p !== 'object' || p === null || typeof (p as { regex?: unknown }).regex !== 'string') {
      return `pattern ${i + 1}: each entry needs a "regex" string`
    }
    try {
      new RegExp((p as { regex: string }).regex)
    } catch (e) {
      return `pattern ${i + 1} has an invalid regex: ${(e as Error).message}`
    }
  }
  return null
}

export function ComplianceConfigPage() {
  const qc = useQueryClient()
  const { data, isLoading, isError, refetch } = useQuery({
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

  const save = useAppMutation({
    mutationFn: (patch: Partial<ComplianceConfig>) => updateComplianceConfig(patch),
    onSuccess: (next) => {
      qc.setQueryData(['compliance-config'], next)
      setDraft(next)
      toast.success('Compliance config saved')
    },
    onError: () => toast.error('Save failed'),
  })

  if (isError) {
    return <ErrorState message="Could not load the detection rules." onRetry={() => void refetch()} />
  }
  if (isLoading || !draft) return <div className="p-6 text-sm text-muted-foreground">Loading…</div>

  // Recomputed each render from the live textarea contents — never stale.
  const overridesError = validateOverrides(overridesText)
  const patternsError = validatePatterns(patternsText)

  const onSave = () => {
    if (overridesError) {
      toast.error(`Risk overrides: ${overridesError}`)
      return
    }
    if (patternsError) {
      toast.error(`Custom patterns: ${patternsError}`)
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
      pii_entity_risk_overrides: JSON.parse(overridesText),
      custom_patterns: JSON.parse(patternsText),
    })
  }

  return (
    <div className="max-w-3xl">
      <PageHeader variant="section"
        title="Compliance config"
        description="Per-tenant scanning thresholds, notification routing, PHI opt-in, and custom regex patterns."
      />

      <div className="mt-6 space-y-6 rounded border border-border p-5">
        <Toggle
          label="Enable compliance scanning"
          checked={draft.enabled}
          onChange={(v) => setDraft({ ...draft, enabled: v })}
        />
        <Toggle
          label="Auto-flag for legal hold on critical findings"
          help="Marks the document for hold review only — actual hold is placed manually via the legal-hold UI."
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
            aria-invalid={!!overridesError}
            className={`mt-2 w-full rounded border bg-muted/40 p-2 font-mono text-xs ${overridesError ? 'border-destructive' : 'border-border'}`}
          />
          {overridesError && <p className="mt-1 text-xs text-destructive">{overridesError}</p>}
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
            aria-invalid={!!patternsError}
            className={`mt-2 w-full rounded border bg-muted/40 p-2 font-mono text-xs ${patternsError ? 'border-destructive' : 'border-border'}`}
          />
          {patternsError && <p className="mt-1 text-xs text-destructive">{patternsError}</p>}
        </div>

        <div className="flex justify-end">
          <Button onClick={onSave} disabled={save.isPending || !!overridesError || !!patternsError}>
            <Save className="me-2 h-4 w-4" />
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

// Merged surface — this standalone URL redirects into the canonical
// tabbed page (/admin/pii-scanning?tab=config). The page component stays
// exported so the shell can embed it: one rendering, one URL.
export const Route = createFileRoute('/_authenticated/admin/intelligence/compliance-config')({
  beforeLoad: () => {
    throw redirect({ to: '/admin/pii-scanning', search: { tab: 'config' }, replace: true })
  },
})
