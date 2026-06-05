// ADR 0062 — LDAP/AD direct-bind admin page.
//
// One page, three sections:
//   1. Connection — URL, transport, bind DN, bind password, search
//      bases, filters. Includes the"Test connection" button that
//      hits POST /admin/ldap/test-bind for a dry-run.
//   2. Group mappings — AD group DN → DMS group selector, with
//      add/remove. Pulls DMS groups from /admin/groups.
//   3. Sync history — most recent ~50 runs from
//      GET /admin/ldap/configs/{id}/history."Sync now" button
//      runs SyncTenant synchronously.
//
// Bind passwords are write-only. Existing config rows show
//"•••••••• (set)" instead of round-tripping the secret. To rotate,
// the admin types a new password into the field — leaving it empty
// on PATCH preserves the stored value.
import { useEffect, useMemo, useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import {
  Network, ShieldCheck, AlertTriangle, CheckCircle2, RefreshCw,
  Trash2, Plus, KeyRound,
} from 'lucide-react'

import {
  listLDAPConfigs,
  createLDAPConfig,
  updateLDAPConfig,
  deleteLDAPConfig,
  testLDAPBind,
  listLDAPMappings,
  addLDAPMapping,
  deleteLDAPMapping,
  listLDAPHistory,
  syncLDAPNow,
  type LDAPConfig,
  type LDAPWriteBody,
  type LDAPTestBindResult,
} from '@/api/ldap'
import { listGroups } from '@/api/groups'
import { readErrorMessage } from '@/api/client'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { LabeledSelect as Select } from '@/components/ui/shadcn/select'
import { Spinner } from '@/components/ui/Spinner'
import { Badge } from '@/components/ui/shadcn/badge'
import { EmptyState } from '@/components/ui/EmptyState'

export const Route = createFileRoute('/_authenticated/admin/tenant/identity/ldap')({
  component: LDAPAdminPage,
})

const DEFAULT_BODY: LDAPWriteBody = {
  url: 'ldaps://ldap.example.com:636',
  use_starttls: true,
  allow_insecure: false,
  bind_dn: 'cn=admin,dc=example,dc=com',
  bind_password: '',
  user_search_base: 'dc=example,dc=com',
  user_search_filter: '(sAMAccountName={username})',
  email_attribute: 'mail',
  display_name_attribute: 'displayName',
  group_search_base: 'dc=example,dc=com',
  group_search_filter: '(member={user_dn})',
  nested_groups: true,
  fallback_to_local: false,
  is_active: false,
}

export function LDAPAdminPage() {
  const qc = useQueryClient()
  const { data: configs, isLoading } = useQuery({
    queryKey: ['admin', 'ldap', 'configs'],
    queryFn: listLDAPConfigs,
  })

  // Active config (or first in the list when none active) drives
  // the form + mapping/history panels.
  const active = useMemo(() => {
    if (!configs?.length) return null
    return configs.find((c) => c.is_active) ?? configs[0]
  }, [configs])

  return (
    <div className="space-y-6">
      <PageHeader
        title="LDAP / Active Directory"
        description="Authenticate against an enterprise directory. Falls back to local password when configured."
      />
      {isLoading ? (
        <Spinner />
      ) : !configs?.length ? (
        <NoConfigYet onCreated={() => qc.invalidateQueries({ queryKey: ['admin', 'ldap'] })} />
      ) : (
        <ConfiguredView config={active!} />
      )}
    </div>
  )
}

// ---- Empty state ---------------------------------------------------------

function NoConfigYet({ onCreated }: { onCreated: () => void }) {
  const [body, setBody] = useState<LDAPWriteBody>(DEFAULT_BODY)
  const create = useAppMutation({
    mutationFn: (b: LDAPWriteBody) => createLDAPConfig(b),
    onSuccess: () => {
      toast.success('LDAP config saved as draft. Test the connection, then activate.')
      onCreated()
    },
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? 'Could not create LDAP config'),
  })

  return (
    <div className="rounded-lg border border-border bg-card p-6">
      <EmptyState
        icon={<Network className="h-10 w-10" />}
        title="No LDAP integration yet"
        description="Add the directory's URL, a service-account bind DN, and search bases. The config is saved as a draft — you'll test and activate it next."
      />
      <div className="mt-6">
        <ConfigForm body={body} setBody={setBody} draftMode submitLabel="Save draft" submitting={create.isPending}
          onSubmit={() => create.mutate(body)} />
      </div>
    </div>
  )
}

// ---- Configured view -----------------------------------------------------

function ConfiguredView({ config }: { config: LDAPConfig }) {
  return (
    <div className="space-y-6">
      <ConnectionSection config={config} />
      <MappingsSection config={config} />
      <HistorySection config={config} />
    </div>
  )
}

function ConnectionSection({ config }: { config: LDAPConfig }) {
  const qc = useQueryClient()
  const [body, setBody] = useState<LDAPWriteBody>(() => ({
    url: config.url,
    use_starttls: config.use_starttls,
    allow_insecure: config.allow_insecure,
    bind_dn: config.bind_dn,
    bind_password: '', // never round-trip
    user_search_base: config.user_search_base,
    user_search_filter: config.user_search_filter,
    email_attribute: config.email_attribute,
    display_name_attribute: config.display_name_attribute,
    group_search_base: config.group_search_base,
    group_search_filter: config.group_search_filter,
    nested_groups: config.nested_groups,
    fallback_to_local: config.fallback_to_local,
    is_active: config.is_active,
  }))

  const update = useAppMutation({
    mutationFn: (b: Partial<LDAPWriteBody>) => updateLDAPConfig(config.id, b),
    onSuccess: () => {
      toast.success('LDAP config saved')
      qc.invalidateQueries({ queryKey: ['admin', 'ldap'] })
    },
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? 'Could not save LDAP config'),
  })

  const remove = useAppMutation({
    mutationFn: () => deleteLDAPConfig(config.id),
    onSuccess: () => {
      toast.success('LDAP config deleted')
      qc.invalidateQueries({ queryKey: ['admin', 'ldap'] })
    },
  })

  const onSubmit = () => {
    // Don't transmit empty password — server preserves the existing
    // sealed value when bind_password is omitted.
    const payload: Partial<LDAPWriteBody> = { ...body }
    if (!payload.bind_password) delete payload.bind_password
    update.mutate(payload)
  }

  return (
    <Section icon={<Network className="h-5 w-5" />} title="Connection">
      <div className="mb-4 flex items-center gap-3 text-sm">
        <Badge variant={config.is_active ? 'success' : 'secondary'}>
          {config.is_active ? 'Active' : 'Draft'}
        </Badge>
        {config.has_bind_password && (
          <span className="text-muted-foreground">
            <KeyRound className="me-1 inline h-3 w-3" />
            Bind password: <code>•••••••• (set)</code>
          </span>
        )}
        {config.last_sync_at && (
          <span className="text-muted-foreground">
            Last sync: {new Date(config.last_sync_at).toLocaleString()}{' '}
            {config.last_sync_status && <Badge variant={syncBadge(config.last_sync_status)}>{config.last_sync_status}</Badge>}
          </span>
        )}
      </div>

      {config.allow_insecure && !config.use_starttls && (
        <div className="mb-4 flex items-start gap-2 rounded border border-red-300 bg-destructive/10 p-3 text-sm text-red-800">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
          <div>
            <strong>Plain ldap:// without StartTLS is in use.</strong> All bind
            traffic — including end-user passwords — is sent in cleartext.
            This setting is intended for closed-network integration tests
            only. Switch to ldaps:// or enable StartTLS for any non-throwaway
            deployment.
          </div>
        </div>
      )}

      <ConfigForm
        body={body}
        setBody={setBody}
        existingId={config.id}
        submitLabel="Save changes"
        submitting={update.isPending}
        onSubmit={onSubmit}
        secondaryAction={
          <Button variant="ghost" onClick={() => {
            if (confirm('Delete this LDAP configuration? Users authenticated via LDAP will fall back to local passwords (or fail to log in if their account has no local password).')) {
              remove.mutate()
            }
          }}>
            <Trash2 className="h-4 w-4" /> Delete
          </Button>
        }
      />
    </Section>
  )
}

// ---- Reusable form -------------------------------------------------------

interface ConfigFormProps {
  body: LDAPWriteBody
  setBody: (b: LDAPWriteBody) => void
  existingId?: string
  draftMode?: boolean
  submitLabel: string
  submitting: boolean
  onSubmit: () => void
  secondaryAction?: React.ReactNode
}

function ConfigForm({ body, setBody, existingId, draftMode, submitLabel, submitting, onSubmit, secondaryAction }: ConfigFormProps) {
  const [testResult, setTestResult] = useState<LDAPTestBindResult | null>(null)
  const [sampleUser, setSampleUser] = useState('')
  const [samplePass, setSamplePass] = useState('')

  const test = useAppMutation({
    mutationFn: () => testLDAPBind(
      existingId
        ? { existing_config_id: existingId, sample_username: sampleUser, sample_password: samplePass }
        : { draft: body, sample_username: sampleUser, sample_password: samplePass },
    ),
    onSuccess: (res) => {
      setTestResult(res)
      if (res.ok) toast.success('Test bind succeeded')
      else toast.error(res.error || 'Test bind failed')
    },
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? 'LDAP test failed'),
  })

  return (
    <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
      <Field label="LDAP URL" hint="ldap:// or ldaps://. ldaps:// recommended.">
        <Input value={body.url} onChange={(e) => setBody({ ...body, url: e.target.value })} />
      </Field>
      <Field label="Bind DN" hint="Service account DN used for searches.">
        <Input value={body.bind_dn} onChange={(e) => setBody({ ...body, bind_dn: e.target.value })} />
      </Field>

      <Field
        label={draftMode ? 'Bind password' : 'New bind password'}
        hint={draftMode ? 'Required.' : 'Leave blank to keep the existing password.'}
      >
        <Input
          type="password"
          value={body.bind_password ?? ''}
          onChange={(e) => setBody({ ...body, bind_password: e.target.value })}
          placeholder={draftMode ? '' : 'Unchanged'}
        />
      </Field>

      <Field label="Transport">
        <div className="flex flex-col gap-2 text-sm">
          <label className="flex items-center gap-2">
            <input type="checkbox" checked={!!body.use_starttls}
              onChange={(e) => setBody({ ...body, use_starttls: e.target.checked })} />
            Use StartTLS for ldap://
          </label>
          <label className="flex items-center gap-2 text-destructive">
            <input type="checkbox" checked={!!body.allow_insecure}
              onChange={(e) => setBody({ ...body, allow_insecure: e.target.checked })} />
            Allow plain ldap:// (cleartext) — testing only
          </label>
        </div>
      </Field>

      <Field label="User search base">
        <Input value={body.user_search_base} onChange={(e) => setBody({ ...body, user_search_base: e.target.value })} />
      </Field>
      <Field label="User search filter" hint="Use {username} as a placeholder.">
        <Input value={body.user_search_filter} onChange={(e) => setBody({ ...body, user_search_filter: e.target.value })} />
      </Field>

      <Field label="Email attribute">
        <Input value={body.email_attribute ?? 'mail'} onChange={(e) => setBody({ ...body, email_attribute: e.target.value })} />
      </Field>
      <Field label="Display-name attribute">
        <Input value={body.display_name_attribute ?? 'displayName'} onChange={(e) => setBody({ ...body, display_name_attribute: e.target.value })} />
      </Field>

      <Field label="Group search base">
        <Input value={body.group_search_base} onChange={(e) => setBody({ ...body, group_search_base: e.target.value })} />
      </Field>
      <Field label="Group search filter" hint="Use {user_dn} as a placeholder.">
        <Input value={body.group_search_filter} onChange={(e) => setBody({ ...body, group_search_filter: e.target.value })} />
      </Field>

      <Field label="Behaviour">
        <div className="flex flex-col gap-2 text-sm">
          <label className="flex items-center gap-2">
            <input type="checkbox" checked={!!body.nested_groups}
              onChange={(e) => setBody({ ...body, nested_groups: e.target.checked })} />
            Resolve nested groups (AD: matching-rule-in-chain)
          </label>
          <label className="flex items-center gap-2">
            <input type="checkbox" checked={!!body.fallback_to_local}
              onChange={(e) => setBody({ ...body, fallback_to_local: e.target.checked })} />
            Fall back to local password on LDAP rejection
          </label>
          <label className="flex items-center gap-2">
            <input type="checkbox" checked={!!body.is_active}
              onChange={(e) => setBody({ ...body, is_active: e.target.checked })} />
            Active (route logins through LDAP)
          </label>
        </div>
      </Field>

      <Field label="Test bind (optional)" hint="Verify by binding as a real directory user. Leave blank to skip.">
        <div className="flex flex-col gap-2">
          <Input placeholder="sample username" value={sampleUser} onChange={(e) => setSampleUser(e.target.value)} />
          <Input placeholder="sample password" type="password" value={samplePass} onChange={(e) => setSamplePass(e.target.value)} />
        </div>
      </Field>

      <div className="md:col-span-2 flex flex-wrap items-center gap-2 pt-2">
        <Button onClick={onSubmit} disabled={submitting}>
          {submitting ? <Spinner /> : submitLabel}
        </Button>
        <Button variant="ghost" onClick={() => test.mutate()} disabled={test.isPending}>
          {test.isPending ? <Spinner /> : <ShieldCheck className="h-4 w-4" />}
          Test connection
        </Button>
        {secondaryAction}
        {testResult && (
          <span className="ms-auto flex items-center gap-2 text-sm">
            {testResult.ok ? <CheckCircle2 className="h-4 w-4 text-green-600" /> : <AlertTriangle className="h-4 w-4 text-destructive" />}
            {testResult.ok
              ? `bind ok · user_found=${testResult.user_found} · groups=${testResult.groups_found}`
              : (testResult.error ?? 'failed')}
          </span>
        )}
      </div>
    </div>
  )
}

// ---- Mappings ------------------------------------------------------------

function MappingsSection({ config }: { config: LDAPConfig }) {
  const qc = useQueryClient()
  const { data: mappings } = useQuery({
    queryKey: ['admin', 'ldap', 'mappings', config.id],
    queryFn: () => listLDAPMappings(config.id),
  })
  const { data: dmsGroups } = useQuery({
    queryKey: ['admin', 'groups'],
    queryFn: listGroups,
  })

  const [ldapDN, setLdapDN] = useState('')
  const [dmsId, setDmsId] = useState('')

  const add = useAppMutation({
    mutationFn: () => addLDAPMapping(config.id, ldapDN, dmsId),
    onSuccess: () => {
      setLdapDN(''); setDmsId('')
      toast.success('Mapping added')
      qc.invalidateQueries({ queryKey: ['admin', 'ldap', 'mappings', config.id] })
    },
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? 'Could not add mapping'),
  })

  const del = useAppMutation({
    mutationFn: (m: { ldap_group_dn: string; dms_group_id: string }) =>
      deleteLDAPMapping(config.id, m.ldap_group_dn, m.dms_group_id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['admin', 'ldap', 'mappings', config.id] }),
  })

  const dmsOptions = (dmsGroups ?? []).map((g) => ({ value: g.id, label: g.name }))

  return (
    <Section icon={<Network className="h-5 w-5" />} title="Group mappings"
      hint="Map AD/LDAP group DNs to DMS groups. Unmapped LDAP groups are ignored.">
      <div className="overflow-hidden rounded border border-border">
        <table className="w-full text-sm">
          <thead className="bg-muted">
            <tr>
              <th className="px-3 py-2 text-start">LDAP group DN</th>
              <th className="px-3 py-2 text-start">DMS group</th>
              <th className="w-12" />
            </tr>
          </thead>
          <tbody>
            {(mappings ?? []).map((m) => {
              const dms = dmsGroups?.find((g) => g.id === m.dms_group_id)
              return (
                <tr key={`${m.ldap_group_dn}|${m.dms_group_id}`} className="border-t border-border">
                  <td className="px-3 py-2 font-mono text-xs">{m.ldap_group_dn}</td>
                  <td className="px-3 py-2">{dms?.name ?? <code className="text-xs">{m.dms_group_id}</code>}</td>
                  <td className="px-3 py-2 text-end">
                    <button
                      onClick={() => del.mutate(m)}
                      aria-label={`Remove mapping for ${m.ldap_group_dn}`}
                      title="Remove mapping"
                      className="text-muted-foreground hover:text-destructive"
                    >
                      <Trash2 className="h-4 w-4" aria-hidden="true" />
                    </button>
                  </td>
                </tr>
              )
            })}
            {!mappings?.length && (
              <tr><td colSpan={3} className="px-3 py-8 text-center text-muted-foreground">No mappings yet.</td></tr>
            )}
          </tbody>
        </table>
      </div>

      <div className="mt-4 flex flex-wrap items-end gap-2">
        <Input placeholder="cn=engineers,ou=groups,dc=example,dc=com"
          className="min-w-[20rem] flex-1" value={ldapDN}
          onChange={(e) => setLdapDN(e.target.value)} />
        <Select
          value={dmsId}
          onValueChange={setDmsId}
          options={dmsOptions}
          placeholder="DMS group"
          className="min-w-[12rem]"
        />
        <Button onClick={() => add.mutate()} disabled={!ldapDN || !dmsId || add.isPending}>
          <Plus className="h-4 w-4" /> Add mapping
        </Button>
      </div>
    </Section>
  )
}

// ---- History -------------------------------------------------------------

function HistorySection({ config }: { config: LDAPConfig }) {
  const qc = useQueryClient()
  const { data, refetch } = useQuery({
    queryKey: ['admin', 'ldap', 'history', config.id],
    queryFn: () => listLDAPHistory(config.id),
  })

  const sync = useAppMutation({
    mutationFn: () => syncLDAPNow(config.id),
    onSuccess: () => {
      toast.success('Sync run complete')
      qc.invalidateQueries({ queryKey: ['admin', 'ldap'] })
    },
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? 'LDAP sync failed'),
  })

  // Refresh on mount + every 15 s while a 'running' row is in flight.
  useEffect(() => {
    const t = setInterval(() => refetch(), 15000)
    return () => clearInterval(t)
  }, [refetch])

  return (
    <Section icon={<RefreshCw className="h-5 w-5" />} title="Sync history"
      hint="The scheduler runs every 15 minutes by default. Use 'Sync now' for an on-demand pull.">
      <div className="mb-4">
        <Button onClick={() => sync.mutate()} disabled={sync.isPending}>
          {sync.isPending ? <Spinner /> : <RefreshCw className="h-4 w-4" />}
          Sync now
        </Button>
      </div>
      <div className="overflow-hidden rounded border border-border">
        <table className="w-full text-sm">
          <thead className="bg-muted">
            <tr>
              <th className="px-3 py-2 text-start">Started</th>
              <th className="px-3 py-2 text-start">Trigger</th>
              <th className="px-3 py-2 text-start">Status</th>
              <th className="px-3 py-2 text-end">Users</th>
              <th className="px-3 py-2 text-end">Groups</th>
              <th className="px-3 py-2 text-end">Errors</th>
            </tr>
          </thead>
          <tbody>
            {(data ?? []).map((h) => (
              <tr key={h.id} className="border-t border-border">
                <td className="px-3 py-2">{new Date(h.started_at).toLocaleString()}</td>
                <td className="px-3 py-2"><code className="text-xs">{h.trigger}</code></td>
                <td className="px-3 py-2"><Badge variant={syncBadge(h.status)}>{h.status}</Badge></td>
                <td className="px-3 py-2 text-end">{h.users_synced}</td>
                <td className="px-3 py-2 text-end">{h.groups_synced}</td>
                <td className="px-3 py-2 text-end">{h.errors}</td>
              </tr>
            ))}
            {!data?.length && (
              <tr><td colSpan={6} className="px-3 py-8 text-center text-muted-foreground">No sync runs yet.</td></tr>
            )}
          </tbody>
        </table>
      </div>
    </Section>
  )
}

// ---- shared chrome -------------------------------------------------------

function Section({ icon, title, hint, children }: { icon: React.ReactNode; title: string; hint?: string; children: React.ReactNode }) {
  return (
    <section className="rounded-lg border border-border bg-card p-6">
      <div className="mb-4 flex items-start justify-between gap-4">
        <div>
          <h2 className="flex items-center gap-2 text-lg font-semibold">{icon}{title}</h2>
          {hint && <p className="mt-1 text-sm text-muted-foreground">{hint}</p>}
        </div>
      </div>
      {children}
    </section>
  )
}

function Field({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <label className="flex flex-col gap-1.5 text-sm">
      <span className="font-medium">{label}</span>
      {children}
      {hint && <span className="text-xs text-muted-foreground">{hint}</span>}
    </label>
  )
}

function syncBadge(status: string): 'success' | 'warning' | 'destructive' | 'secondary' {
  switch (status) {
    case 'ok': return 'success'
    case 'partial': return 'warning'
    case 'error': return 'destructive'
    default: return 'secondary'
  }
}
