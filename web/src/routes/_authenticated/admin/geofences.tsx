import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { Globe, Plus, Trash2, X as XIcon } from 'lucide-react'

import {
  createGeofence,
  deleteGeofence,
  dryRunGeofence,
  listGeofences,
  type Geofence,
  type GeofenceMode,
  type GeofenceScope,
} from '@/api/geofences'
import { PageHeader } from '@/components/shared/PageHeader'
import { EmptyState } from '@/components/ui/EmptyState'
import { Button } from '@/components/ui/Button'
import { Input } from '@/components/ui/Input'
import { Textarea } from '@/components/ui/Textarea'
import { Skeleton } from '@/components/ui/Skeleton'
import { Badge } from '@/components/ui/Badge'

function GeofencesPage() {
  const [creating, setCreating] = useState(false)
  const qc = useQueryClient()
  const { data, isLoading } = useQuery({
    queryKey: ['geofences'],
    queryFn: listGeofences,
  })
  const del = useMutation({
    mutationFn: (id: string) => deleteGeofence(id),
    onSuccess: () => {
      toast.success('Policy removed')
      qc.invalidateQueries({ queryKey: ['geofences'] })
    },
    onError: () => toast.error('Delete failed'),
  })

  return (
    <div>
      <PageHeader
        title="Geofences"
        description="Block or require step-up MFA by country and CIDR."
        actions={
          <Button onClick={() => setCreating(true)}>
            <Plus className="mr-1 h-4 w-4" aria-hidden="true" />
            New policy
          </Button>
        }
      />

      {creating && (
        <CreateGeofenceForm
          onClose={() => setCreating(false)}
          onCreated={() => {
            setCreating(false)
            qc.invalidateQueries({ queryKey: ['geofences'] })
          }}
        />
      )}

      <DryRunTester />

      {isLoading && (
        <div role="status" aria-live="polite" className="mt-4 space-y-2">
          <Skeleton className="h-16 w-full" />
          <Skeleton className="h-16 w-full" />
        </div>
      )}

      {!isLoading && (!data || data.length === 0) && (
        <EmptyState
          icon={<Globe className="h-10 w-10" />}
          title="No policies"
          description="Add a policy to restrict tenant / workspace / document access by country or CIDR."
        />
      )}

      {!isLoading && data && data.length > 0 && (
        <ul className="mt-4 space-y-2" aria-label="Geofence policies">
          {data.map((g) => (
            <GeofenceRow key={g.id} geofence={g} onDelete={() => del.mutate(g.id)} busy={del.isPending} />
          ))}
        </ul>
      )}
    </div>
  )
}

function GeofenceRow({
  geofence,
  onDelete,
  busy,
}: {
  geofence: Geofence
  onDelete: () => void
  busy: boolean
}) {
  return (
    <li className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4">
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <Badge variant={geofence.mode === 'deny' ? 'disposed' : geofence.mode === 'step_up' ? 'in_review' : 'active'}>
              {geofence.mode}
            </Badge>
            <Badge variant="default">{geofence.scope}</Badge>
            <Badge variant="default">apply: {geofence.apply_to}</Badge>
            {geofence.country_codes && geofence.country_codes.length > 0 && (
              <span className="text-xs text-[var(--color-text-secondary)]">
                Countries: {geofence.country_codes.join(', ')}
              </span>
            )}
          </div>
          {(geofence.cidr_allowlist?.length || geofence.cidr_denylist?.length) && (
            <p className="mt-1 text-xs text-[var(--color-text-secondary)]">
              {geofence.cidr_allowlist?.length
                ? `Allow CIDRs: ${geofence.cidr_allowlist.join(', ')} `
                : ''}
              {geofence.cidr_denylist?.length
                ? `Deny CIDRs: ${geofence.cidr_denylist.join(', ')}`
                : ''}
            </p>
          )}
        </div>
        <Button
          variant="ghost"
          onClick={onDelete}
          loading={busy}
          aria-label="Delete policy"
        >
          <Trash2 className="h-4 w-4" aria-hidden="true" />
        </Button>
      </div>
    </li>
  )
}

function CreateGeofenceForm({
  onClose,
  onCreated,
}: {
  onClose: () => void
  onCreated: () => void
}) {
  const [scope, setScope] = useState<GeofenceScope>('tenant')
  const [scopeID, setScopeID] = useState('')
  const [mode, setMode] = useState<GeofenceMode>('deny')
  const [countries, setCountries] = useState('')
  const [allowCIDRs, setAllowCIDRs] = useState('')
  const [denyCIDRs, setDenyCIDRs] = useState('')

  const create = useMutation({
    mutationFn: () =>
      createGeofence({
        scope,
        scope_id: scope === 'tenant' ? undefined : scopeID.trim() || undefined,
        mode,
        country_codes: splitList(countries).map((c) => c.toUpperCase()),
        cidr_allowlist: splitList(allowCIDRs),
        cidr_denylist: splitList(denyCIDRs),
        apply_to: '*',
        enabled: true,
      }),
    onSuccess: () => {
      toast.success('Policy created')
      onCreated()
    },
    onError: (err: unknown) => {
      const msg =
        err && typeof err === 'object' && 'response' in err
          ? // eslint-disable-next-line @typescript-eslint/no-explicit-any
            (err as any).response?.data?.error?.message ?? 'Could not create'
          : 'Could not create'
      toast.error(msg)
    },
  })

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault()
        create.mutate()
      }}
      className="mb-4 space-y-3 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4"
      aria-label="Create geofence policy"
    >
      <div className="flex items-center justify-between">
        <h3 className="text-base font-medium">New geofence policy</h3>
        <button
          type="button"
          onClick={onClose}
          className="rounded-md p-1 hover:bg-slate-100 dark:hover:bg-slate-800"
          aria-label="Cancel"
        >
          <XIcon className="h-4 w-4" aria-hidden="true" />
        </button>
      </div>

      <div className="grid grid-cols-2 gap-3">
        <label className="flex flex-col gap-1 text-sm">
          <span className="font-medium text-[var(--color-text-secondary)]">Scope</span>
          <select
            value={scope}
            onChange={(e) => setScope(e.target.value as GeofenceScope)}
            className="h-9 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-secondary)] px-2"
          >
            <option value="tenant">Tenant</option>
            <option value="workspace">Workspace</option>
            <option value="document">Document</option>
          </select>
        </label>
        <label className="flex flex-col gap-1 text-sm">
          <span className="font-medium text-[var(--color-text-secondary)]">Mode</span>
          <select
            value={mode}
            onChange={(e) => setMode(e.target.value as GeofenceMode)}
            className="h-9 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-secondary)] px-2"
          >
            <option value="deny">Deny</option>
            <option value="allow">Allow</option>
            <option value="step_up">Require step-up</option>
          </select>
        </label>
      </div>

      {scope !== 'tenant' && (
        <Input
          label={scope === 'workspace' ? 'Workspace ID' : 'Document ID'}
          value={scopeID}
          onChange={(e) => setScopeID(e.target.value)}
          required
        />
      )}

      <Input
        label="Country codes (ISO 3166-1 alpha-2, comma separated)"
        placeholder="CN, RU, KP"
        value={countries}
        onChange={(e) => setCountries(e.target.value)}
      />
      <Textarea
        label="CIDR allowlist (one per line)"
        value={allowCIDRs}
        onChange={(e) => setAllowCIDRs(e.target.value)}
        rows={2}
      />
      <Textarea
        label="CIDR denylist (one per line)"
        value={denyCIDRs}
        onChange={(e) => setDenyCIDRs(e.target.value)}
        rows={2}
      />
      <div className="flex justify-end gap-2">
        <Button variant="ghost" type="button" onClick={onClose}>
          Cancel
        </Button>
        <Button type="submit" loading={create.isPending}>
          Create
        </Button>
      </div>
    </form>
  )
}

function DryRunTester() {
  const [ip, setIp] = useState('')
  const [result, setResult] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)

  const run = async (e: React.FormEvent) => {
    e.preventDefault()
    setLoading(true)
    setResult(null)
    try {
      const res = await dryRunGeofence({ ip: ip.trim() })
      const parts = [
        res.allow ? 'ALLOW' : 'DENY',
        res.require_step_up ? '+ STEP-UP' : '',
        `(${res.reason})`,
        res.matched_policy_id ? `matched ${res.matched_policy_id.slice(0, 8)}` : '',
      ].filter(Boolean)
      setResult(parts.join(' '))
    } catch (err: unknown) {
      const msg =
        err && typeof err === 'object' && 'response' in err
          ? // eslint-disable-next-line @typescript-eslint/no-explicit-any
            (err as any).response?.data?.error?.message ?? 'Dry-run failed'
          : 'Dry-run failed'
      setResult(`error: ${msg}`)
    } finally {
      setLoading(false)
    }
  }

  return (
    <form
      onSubmit={run}
      className="mt-4 flex items-end gap-3 rounded-lg border border-dashed border-[var(--color-border)] bg-[var(--color-bg)] p-4"
      aria-label="Dry-run geofence decision"
    >
      <Input
        label="Test an IP"
        placeholder="203.0.113.5"
        value={ip}
        onChange={(e) => setIp(e.target.value)}
        required
      />
      <Button type="submit" loading={loading}>
        Evaluate
      </Button>
      {result && (
        <output
          className="ml-3 rounded-md bg-slate-100 px-3 py-1 text-xs dark:bg-slate-800"
          aria-live="polite"
        >
          {result}
        </output>
      )}
    </form>
  )
}

function splitList(s: string): string[] {
  return s
    .split(/[\s,]+/)
    .map((x) => x.trim())
    .filter(Boolean)
}

export const Route = createFileRoute('/_authenticated/admin/geofences')({
  component: GeofencesPage,
})
