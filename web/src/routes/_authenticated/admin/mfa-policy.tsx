import { createFileRoute } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { ShieldCheck, Lock, Info } from 'lucide-react'

import { PageHeader } from '@/components/shared/PageHeader'
import { Card } from '@/components/ui/card'
import { Button } from '@/components/ui/shadcn/button'
import { Spinner } from '@/components/ui/Spinner'
import { Badge } from '@/components/ui/shadcn/badge'
import { readErrorMessage } from '@/api/client'
import { useAuthStore } from '@/store/authStore'
import { getMFAPolicy, saveMFAPolicy, type MFAMode, type TenantMFAPolicy } from '@/api/mfaPolicy'

// /admin/mfa-policy — set the tenant-wide multi-factor authentication
// requirement. Backed by GET/PUT /api/v1/admin/mfa/policy (auth service,
// ADR 0063). The login flow consults LoadMFAPolicy and forces enrolment
// when mode === 'required' before issuing the session cookie.
//
// Three modes:
//   - optional:    users can voluntarily enrol; login proceeds without MFA
//   - required:    every user must have at least one strong factor; login
//                  short-circuits to /mfa/enrol until they do
//   - conditional: required only for the actions in conditional_actions
//                  (e.g. ['settings_change','share_link_create']); admins
//                  can pick which sensitive operations re-prompt for a
//                  factor without forcing baseline login-time enrolment

const METHOD_OPTIONS: { id: string; label: string; description: string }[] = [
  { id: 'passkey', label: 'Passkey / WebAuthn', description: 'Hardware-bound key (YubiKey, Touch ID, Windows Hello). Strongest factor — phishing-resistant.' },
  { id: 'totp',    label: 'Authenticator app (TOTP)', description: 'Google Authenticator, 1Password, Authy. Time-based 6-digit codes.' },
  { id: 'sms',     label: 'SMS (one-time code)', description: 'Discouraged — vulnerable to SIM-swap. Only if the tenant has Twilio credentials configured.' },
  { id: 'email',   label: 'Email (one-time code)', description: 'Last-resort fallback. Same trust as the user\'s mailbox.' },
]

function MFAPolicyPage() {
  const qc = useQueryClient()
  const role = useAuthStore((s) => s.user?.role)
  const canManage = role === 'owner' || role === 'admin'

  const { data, isLoading } = useQuery({
    queryKey: ['admin-mfa-policy'],
    queryFn: getMFAPolicy,
    enabled: canManage,
  })

  const [draft, setDraft] = useState<TenantMFAPolicy | null>(null)
  useEffect(() => {
    if (data && draft === null) setDraft(data)
  }, [data, draft])

  const save = useAppMutation({
    mutationFn: (p: TenantMFAPolicy) => saveMFAPolicy(p),
    onSuccess: () => {
      toast.success('MFA policy saved — new sessions enforce immediately')
      qc.invalidateQueries({ queryKey: ['admin-mfa-policy'] })
    },
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? 'Save failed'),
  })

  if (!canManage) {
    return (
      <div>
        <PageHeader title="MFA policy" description="Multi-factor authentication enforcement for this tenant" />
        <Card className="p-8 text-center text-sm text-muted-foreground">
          MFA policy is an administrator surface.
        </Card>
      </div>
    )
  }
  if (isLoading || draft === null) {
    return <div className="flex justify-center py-12"><Spinner className="h-6 w-6" /></div>
  }

  const toggleMethod = (id: string) => {
    const has = draft.allowed_methods.includes(id)
    setDraft({
      ...draft,
      allowed_methods: has
        ? draft.allowed_methods.filter((m) => m !== id)
        : [...draft.allowed_methods, id],
    })
  }

  const dirty = JSON.stringify(draft) !== JSON.stringify(data)

  return (
    <div className="space-y-4">
      <PageHeader
        title="MFA policy"
        description="Tenant-wide multi-factor authentication enforcement. New sessions consult the policy immediately; in-flight sessions keep their current state."
        actions={
          <div className="flex items-center gap-2">
            <Badge variant={draft.mode === 'required' ? 'active' : draft.mode === 'conditional' ? 'in_review' : 'draft'}>
              {draft.mode}
            </Badge>
            <Button
              size="sm"
              disabled={!dirty || draft.allowed_methods.length === 0}
              loading={save.isPending}
              onClick={() => save.mutate(draft)}
            >
              Save policy
            </Button>
          </div>
        }
      />

      <Card className="p-4">
        <h3 className="mb-3 flex items-center gap-2 text-sm font-semibold">
          <ShieldCheck className="h-4 w-4 text-primary" />
          Enforcement mode
        </h3>
        <div className="grid gap-2 sm:grid-cols-3">
          {(['optional', 'required', 'conditional'] as MFAMode[]).map((m) => (
            <button
              key={m}
              type="button"
              onClick={() => setDraft({ ...draft, mode: m })}
              className={[
                'flex flex-col items-start gap-1 rounded-xl border p-3 text-start transition-colors',
                draft.mode === m
                  ? 'border-primary bg-primary/10'
                  : 'border-border hover:border-primary/50',
              ].join(' ')}
              data-testid={`mfa-mode-${m}`}
            >
              <span className="text-sm font-medium capitalize">{m}</span>
              <span className="text-xs text-muted-foreground">
                {m === 'optional'    && 'Users can enrol voluntarily; login proceeds without MFA.'}
                {m === 'required'    && 'Every user must have a strong factor. Logins without one are redirected to /mfa/enrol.'}
                {m === 'conditional' && 'Required only for the sensitive actions listed below.'}
              </span>
            </button>
          ))}
        </div>
      </Card>

      <Card className="p-4">
        <h3 className="mb-3 flex items-center gap-2 text-sm font-semibold">
          <Lock className="h-4 w-4 text-primary" />
          Allowed methods
        </h3>
        <ul className="space-y-2">
          {METHOD_OPTIONS.map((opt) => {
            const checked = draft.allowed_methods.includes(opt.id)
            return (
              <li key={opt.id}>
                <label className="flex cursor-pointer items-start gap-3 rounded-lg border border-border p-3 hover:bg-muted/40">
                  <input
                    type="checkbox"
                    checked={checked}
                    onChange={() => toggleMethod(opt.id)}
                    className="mt-1 rounded border-input"
                    data-testid={`mfa-method-${opt.id}`}
                  />
                  <div className="min-w-0 flex-1">
                    <p className="text-sm font-medium">{opt.label}</p>
                    <p className="text-xs text-muted-foreground">{opt.description}</p>
                  </div>
                </label>
              </li>
            )
          })}
        </ul>
        {draft.allowed_methods.length === 0 && (
          <p className="mt-2 text-xs text-destructive">Pick at least one method — saving with zero would lock everyone out.</p>
        )}
      </Card>

      {draft.mode === 'required' && (
        <Card className="flex items-start gap-2 border-info/40 bg-info/5 p-3 text-xs">
          <Info className="mt-0.5 h-4 w-4 shrink-0 text-info" />
          <p className="text-muted-foreground">
            On save, the next login attempt from any user without an enrolled factor is redirected to the enrolment flow.
            Existing sessions keep working until they expire. Audit log records every policy change.
          </p>
        </Card>
      )}
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/mfa-policy')({ component: MFAPolicyPage })
