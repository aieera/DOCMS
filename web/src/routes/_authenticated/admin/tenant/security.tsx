// /admin/tenant/security — tenant-wide session-policy editor (§8.1).
// Owner-gated; non-owners see a read-only view with disabled inputs
// (the backend enforces the same via RequireRole("owner")).

import { createFileRoute } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'

import { PageHeader } from '@/components/shared/PageHeader'
import { AdminGuard } from '@/features/admin/internalAuth/AdminGuard'
import { Button } from '@/components/ui/Button'
import { useAuthStore } from '@/store/authStore'
import {
  getSessionPolicy,
  putSessionPolicy,
  type SessionPolicy,
  type BindingStrictness,
} from '@/api/sessionPolicy'

const DEFAULT_POLICY: SessionPolicy = {
  ttl_hours: 24,
  sliding_minutes: 60,
  absolute_max_days: 7,
  concurrent_limit: 5,
  binding_strictness: 'warn',
}

function TenantSecurityPage() {
  const user = useAuthStore((s) => s.user)
  const canEdit = user?.role === 'owner'
  const qc = useQueryClient()
  const { data, isLoading, isError } = useQuery({
    queryKey: ['tenant', 'session-policy'],
    queryFn: getSessionPolicy,
  })
  const [form, setForm] = useState<SessionPolicy>(DEFAULT_POLICY)
  useEffect(() => {
    if (data) setForm(data)
  }, [data])

  const save = useMutation({
    mutationFn: (p: SessionPolicy) => putSessionPolicy(p),
    onSuccess: () => {
      toast.success('Session policy saved')
      qc.invalidateQueries({ queryKey: ['tenant', 'session-policy'] })
    },
    onError: (e) => toast.error(`Save failed: ${String(e)}`),
  })

  return (
    <AdminGuard>
      <div className="space-y-8">
        <PageHeader
          title="Session security policy"
          description="Tenant-wide session lifetime + binding rules (Blueprint §8.1). Changes apply to all users on next login or refresh."
        />

        {isLoading && <p className="text-sm text-[var(--color-text-secondary)]">Loading policy…</p>}
        {isError && (
          <div role="alert" className="rounded-lg border border-red-200 bg-red-50 p-4 text-sm text-red-900 dark:bg-red-950/40 dark:text-red-200">
            Failed to load session policy.
          </div>
        )}

        {data && (
          <form
            data-testid="session-policy-form"
            onSubmit={(e) => {
              e.preventDefault()
              save.mutate(form)
            }}
            className="space-y-6 max-w-2xl"
          >
            <RangeField
              label="Session TTL (hours)"
              testId="ttl-hours"
              min={1}
              max={720}
              value={form.ttl_hours}
              disabled={!canEdit}
              onChange={(v) => setForm((f) => ({ ...f, ttl_hours: v }))}
              hint="How long a new session is valid before it requires refresh. Default 24h."
            />
            <NumberField
              label="Sliding extension (minutes)"
              testId="sliding-minutes"
              min={5}
              max={1440}
              value={form.sliding_minutes}
              disabled={!canEdit}
              onChange={(v) => setForm((f) => ({ ...f, sliding_minutes: v }))}
              hint="Each authenticated request within this window of expiry extends the session by one full TTL. Default 60."
            />
            <NumberField
              label="Absolute max lifetime (days)"
              testId="absolute-max-days"
              min={1}
              max={30}
              value={form.absolute_max_days}
              disabled={!canEdit}
              onChange={(v) => setForm((f) => ({ ...f, absolute_max_days: v }))}
              hint="Hard cap regardless of activity. Session is forced to re-auth after this window. Default 7."
            />
            <NumberField
              label="Concurrent session limit"
              testId="concurrent-limit"
              min={1}
              max={50}
              value={form.concurrent_limit}
              disabled={!canEdit}
              onChange={(v) => setForm((f) => ({ ...f, concurrent_limit: v }))}
              hint="Maximum simultaneously-active sessions per user. Oldest is evicted when exceeded. Default 5."
            />

            <fieldset data-testid="binding-strictness-fieldset" className="space-y-2">
              <legend className="text-sm font-medium">Session binding strictness</legend>
              <p className="text-xs text-[var(--color-text-secondary)]">
                How the server reacts when a session's IP (/24 for v4, /56 for v6) or browser
                fingerprint changes mid-session.
              </p>
              {(['none', 'warn', 'enforce'] as BindingStrictness[]).map((v) => (
                <label key={v} className="flex items-start gap-2 text-sm">
                  <input
                    type="radio"
                    name="binding_strictness"
                    value={v}
                    data-testid={`strictness-${v}`}
                    checked={form.binding_strictness === v}
                    disabled={!canEdit}
                    onChange={() => setForm((f) => ({ ...f, binding_strictness: v }))}
                    className="mt-0.5"
                  />
                  <span>
                    <strong>{v}</strong>{' '}
                    <span className="text-[var(--color-text-secondary)]">— {strictnessDescription(v)}</span>
                  </span>
                </label>
              ))}
            </fieldset>

            <div className="flex items-center gap-3">
              <Button type="submit" disabled={!canEdit} loading={save.isPending} data-testid="save-policy">
                Save policy
              </Button>
              {!canEdit && (
                <span className="text-xs text-[var(--color-text-secondary)]">
                  Only the tenant owner can change these values.
                </span>
              )}
            </div>
          </form>
        )}
      </div>
    </AdminGuard>
  )
}

function RangeField({
  label, testId, min, max, value, disabled, onChange, hint,
}: { label: string; testId: string; min: number; max: number; value: number; disabled?: boolean; onChange: (v: number) => void; hint: string }) {
  return (
    <div>
      <div className="flex items-baseline justify-between">
        <label htmlFor={testId} className="text-sm font-medium">{label}</label>
        <span className="text-sm tabular-nums text-[var(--color-text-secondary)]" data-testid={`${testId}-value`}>
          {value}
        </span>
      </div>
      <input
        id={testId}
        data-testid={testId}
        type="range"
        min={min}
        max={max}
        value={value}
        disabled={disabled}
        onChange={(e) => onChange(parseInt(e.target.value, 10))}
        className="mt-2 w-full"
      />
      <p className="mt-1 text-xs text-[var(--color-text-secondary)]">{hint}</p>
    </div>
  )
}

function NumberField({
  label, testId, min, max, value, disabled, onChange, hint,
}: { label: string; testId: string; min: number; max: number; value: number; disabled?: boolean; onChange: (v: number) => void; hint: string }) {
  return (
    <div>
      <label htmlFor={testId} className="text-sm font-medium">{label}</label>
      <input
        id={testId}
        data-testid={testId}
        type="number"
        min={min}
        max={max}
        value={value}
        disabled={disabled}
        onChange={(e) => onChange(parseInt(e.target.value || '0', 10))}
        className="mt-1 w-32 rounded border border-[var(--color-border)] bg-[var(--color-bg-secondary)] px-2 py-1 text-sm"
      />
      <p className="mt-1 text-xs text-[var(--color-text-secondary)]">{hint}</p>
    </div>
  )
}

function strictnessDescription(v: BindingStrictness): string {
  switch (v) {
    case 'none':
      return 'Log only. No user-visible effect. Use for CI / synthetic-traffic tenants.'
    case 'warn':
      return 'Show a dismissible yellow banner on IP/UA drift. Default — balances UX with visibility.'
    case 'enforce':
      return 'Revoke the session on drift and force re-auth. High-security tenants; noisy on mobile networks.'
  }
}

export const Route = createFileRoute('/_authenticated/admin/tenant/security')({
  component: TenantSecurityPage,
})
