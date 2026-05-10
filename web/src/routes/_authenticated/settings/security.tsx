import { createFileRoute } from '@tanstack/react-router'
import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import {
  Fingerprint, Trash2, ShieldCheck, AlertTriangle, Plus,
  User as UserIcon, Mail, Building2, Monitor, Globe, Clock, LogOut,
} from 'lucide-react'

import {
  registerPasskey,
  listPasskeys,
  deletePasskey,
  isWebAuthnSupported,
  type PasskeyView,
} from '@/api/webauthn'
import {
  listSessions,
  revokeSession,
  revokeAllSessions,
  type SessionRow,
} from '@/api/auth'
import { useAuthStore } from '@/store/authStore'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Dialog } from '@/components/ui/Dialog'
import { Input } from '@/components/ui/shadcn/input'
import { Spinner } from '@/components/ui/Spinner'

// /settings/security — full account-settings hub.
//
// Three stacked sections so a user has one page that covers
// every self-service knob:
//   1. Profile      — read-only summary (display name, email,
//                     role, tenant). Self-edit endpoints don't
//                     exist server-side yet; surfaced as a
//"coming soon" hint rather than hidden so
//                     the section is still useful.
//   2. Security     — passkeys (ADR 0061). Add / remove with
//                     friendly names; prominent CTA on empty state.
//   3. Sessions     — active sessions with revoke. Backed by the
//                     existing GET /auth/sessions endpoint.

function SettingsPage() {
  return (
    <div className="mx-auto max-w-3xl space-y-6">
      <PageHeader
        title="Settings"
        description="Manage your account, security, and active sessions."
      />
      <ProfileSection />
      <SecuritySection />
      <SessionsSection />
    </div>
  )
}

// ---- Profile ---------------------------------------------------------

function ProfileSection() {
  const user = useAuthStore((s) => s.user)
  return (
    <Section title="Profile" icon={<UserIcon className="h-4 w-4" />}>
      <dl className="grid grid-cols-1 gap-3 text-sm md:grid-cols-2">
        <Field label="Display name" icon={<UserIcon  className="h-3 w-3" />} value={user?.display_name} />
        <Field label="Email"        icon={<Mail      className="h-3 w-3" />} value={user?.email} />
        <Field label="Role"         icon={<Building2 className="h-3 w-3" />} value={user?.role} />
        <Field label="Tenant"       icon={<Building2 className="h-3 w-3" />} value={user?.tenant_id ? user.tenant_id.slice(0, 8) + '…' : '—'} />
      </dl>
      <p className="mt-3 text-xs text-muted-foreground">
        Self-service profile editing is coming soon. To update your display name, contact your tenant admin.
      </p>
    </Section>
  )
}

function Field({ label, value, icon }: { label: string; value?: string; icon?: React.ReactNode }) {
  return (
    <div className="flex flex-col">
      <dt className="flex items-center gap-1 text-xs text-muted-foreground">
        {icon}{label}
      </dt>
      <dd className="mt-0.5 break-words font-medium">{value || <em className="text-muted-foreground">not set</em>}</dd>
    </div>
  )
}

// ---- Security (passkeys) --------------------------------------------

function SecuritySection() {
  const supported = isWebAuthnSupported()
  const qc = useQueryClient()
  const [adding, setAdding] = useState(false)
  const [name, setName] = useState('')

  const { data: passkeys, isLoading } = useQuery({
    queryKey: ['passkeys'],
    queryFn: listPasskeys,
  })

  const addMut = useMutation({
    mutationFn: () => registerPasskey(name),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['passkeys'] })
      toast.success('Passkey added')
      setAdding(false)
      setName('')
    },
    onError: (err: { message?: string; response?: { status?: number; data?: { error?: string } } }) => {
      const detail = err.response?.data?.error ?? err.message ?? 'Could not add passkey'
      if (err.response?.status === 501) {
        toast.error('Passkeys not configured for this deploy. Contact your admin.')
      } else {
        toast.error(detail)
      }
    },
  })

  const removeMut = useMutation({
    mutationFn: (credentialID: string) => deletePasskey(credentialID),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['passkeys'] })
      toast.success('Passkey removed')
    },
    onError: () => toast.error('Could not remove'),
  })

  const handleAdd = () => {
    if (!name.trim()) {
      toast.error('Give this passkey a name (e.g."Work laptop")')
      return
    }
    addMut.mutate()
  }

  return (
    <Section
      title="Security"
      icon={<ShieldCheck className="h-4 w-4" />}
      action={
        (passkeys ?? []).length > 0 ? (
          <Button onClick={() => setAdding(true)} disabled={!supported} data-testid="add-passkey">
            <Plus className="h-4 w-4" /> Add passkey
          </Button>
        ) : null
      }
    >
      {!supported && (
        <div className="mb-3 rounded-md border border-warning/40 bg-warning/10 p-3 text-sm text-warning">
          <div className="flex items-start gap-2">
            <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
            <div>Your browser doesn&apos;t support passkeys. Use a recent Chrome, Edge, Safari, or Firefox.</div>
          </div>
        </div>
      )}

      {isLoading ? (
        <div className="flex justify-center py-6"><Spinner className="h-5 w-5" /></div>
      ) : (passkeys ?? []).length === 0 ? (
        <div className="rounded-md border border-dashed border-border p-8 text-center">
          <Fingerprint className="mx-auto mb-3 h-10 w-10 text-primary opacity-70" />
          <p className="mb-1 font-medium">You don&apos;t have any passkeys yet</p>
          <p className="mb-4 text-sm text-muted-foreground">
            Passkeys let you sign in without a password — they&apos;re also phishing-resistant.
            Use your laptop&apos;s fingerprint reader, your phone, or a Yubikey.
          </p>
          <Button onClick={() => setAdding(true)} disabled={!supported} data-testid="add-first-passkey">
            <Plus className="h-4 w-4" /> Add your first passkey
          </Button>
        </div>
      ) : (
        <ul className="space-y-2" data-testid="passkey-list">
          {(passkeys ?? []).map((p) => (
            <PasskeyRow
              key={p.credential_id} p={p}
              onRemove={(id) => removeMut.mutate(id)}
              removing={removeMut.isPending}
            />
          ))}
        </ul>
      )}

      <Dialog
        open={adding}
        onOpenChange={(o) => { if (!o) { setAdding(false); setName('') } }}
        title="Add a passkey"
       
      >
        <div className="space-y-3" data-testid="add-passkey-dialog">
          <p className="text-sm text-muted-foreground">
            Give this passkey a name so you recognize it in the list. The name is local to your account; the authenticator (Yubikey, Touch ID, etc.) doesn&apos;t see it.
          </p>
          <Input
            label="Friendly name"
            placeholder="e.g. Work laptop, Phone, Yubikey at desk"
            value={name}
            onChange={(e) => setName(e.target.value)}
            autoFocus
            data-testid="passkey-name"
          />
          <div className="flex justify-end gap-2 pt-2">
            <Button variant="ghost" onClick={() => { setAdding(false); setName('') }}>Cancel</Button>
            <Button
              onClick={handleAdd}
              disabled={addMut.isPending || !name.trim()}
              data-testid="passkey-confirm"
            >
              {addMut.isPending ? <Spinner className="h-4 w-4" /> : <Fingerprint className="h-4 w-4" />}
              Continue
            </Button>
          </div>
        </div>
      </Dialog>
    </Section>
  )
}

function PasskeyRow({ p, onRemove, removing }: {
  p: PasskeyView
  onRemove: (id: string) => void
  removing: boolean
}) {
  return (
    <li
      className="flex items-center justify-between rounded-md border border-border p-3"
      data-testid={`passkey-row-${p.credential_id}`}
    >
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <Fingerprint className="h-4 w-4 text-primary" />
          <span className="font-medium">{p.name}</span>
          {p.backup_state && (
            <span className="rounded-full bg-emerald-100 px-2 py-0.5 text-xs text-emerald-800 dark:bg-emerald-900 dark:text-emerald-200">
              Synced
            </span>
          )}
        </div>
        <p className="mt-1 text-xs text-muted-foreground">
          {p.transports.length > 0 ? p.transports.join(' · ') : 'unknown transport'}
          {' · added '}{relativeTime(p.created_at)}
          {p.last_used_at ? ` · last used ${relativeTime(p.last_used_at)}` : ' · never used'}
        </p>
      </div>
      <Button
        variant="ghost"
        onClick={() => {
          if (confirm(`Remove passkey"${p.name}"?`)) onRemove(p.credential_id)
        }}
        disabled={removing}
        data-testid={`remove-passkey-${p.credential_id}`}
      >
        <Trash2 className="h-4 w-4 text-destructive" />
      </Button>
    </li>
  )
}

// ---- Active sessions ------------------------------------------------

function SessionsSection() {
  const qc = useQueryClient()
  const { data: sessions, isLoading } = useQuery({
    queryKey: ['sessions'],
    queryFn: listSessions,
  })

  const revokeMut = useMutation({
    mutationFn: (id: string) => revokeSession(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['sessions'] })
      toast.success('Session revoked')
    },
    onError: () => toast.error('Could not revoke'),
  })
  const revokeAllMut = useMutation({
    mutationFn: revokeAllSessions,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['sessions'] })
      toast.success('All other sessions revoked')
    },
    onError: () => toast.error('Could not revoke'),
  })

  return (
    <Section
      title="Active sessions"
      icon={<Monitor className="h-4 w-4" />}
      action={
        (sessions ?? []).length > 1 ? (
          <Button
            variant="ghost"
            onClick={() => {
              if (confirm('Sign out of every other device?')) revokeAllMut.mutate()
            }}
            disabled={revokeAllMut.isPending}
            data-testid="revoke-all-sessions"
          >
            <LogOut className="h-4 w-4" /> Sign out other devices
          </Button>
        ) : null
      }
    >
      {isLoading ? (
        <div className="flex justify-center py-6"><Spinner className="h-5 w-5" /></div>
      ) : (sessions ?? []).length === 0 ? (
        <p className="text-sm text-muted-foreground">No active sessions found.</p>
      ) : (
        <ul className="space-y-2" data-testid="session-list">
          {(sessions ?? []).map((s) => (
            <SessionRowView
              key={s.id}
              s={s}
              onRevoke={() => revokeMut.mutate(s.id)}
              revoking={revokeMut.isPending}
            />
          ))}
        </ul>
      )}
    </Section>
  )
}

function SessionRowView({ s, onRevoke, revoking }: {
  s: SessionRow
  onRevoke: () => void
  revoking: boolean
}) {
  const ua = parseUA(s.user_agent ?? '')
  return (
    <li
      className="flex items-center justify-between rounded-md border border-border p-3"
      data-testid={`session-row-${s.id}`}
    >
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <Monitor className="h-4 w-4 text-primary" />
          <span className="font-medium">{ua}</span>
          {s.current && (
            <span className="rounded-full bg-emerald-100 px-2 py-0.5 text-xs text-emerald-800 dark:bg-emerald-900 dark:text-emerald-200">
              This device
            </span>
          )}
        </div>
        <p className="mt-1 flex items-center gap-3 text-xs text-muted-foreground">
          {s.ip_address && <span className="flex items-center gap-1"><Globe className="h-3 w-3" />{s.ip_address}</span>}
          <span className="flex items-center gap-1"><Clock className="h-3 w-3" />last active {relativeTime(s.last_activity_at)}</span>
        </p>
      </div>
      {!s.current && (
        <Button variant="ghost" onClick={onRevoke} disabled={revoking}>
          <Trash2 className="h-4 w-4 text-destructive" />
        </Button>
      )}
    </li>
  )
}

// ---- shared --------------------------------------------------------

function Section({ title, icon, action, children }: {
  title: string
  icon?: React.ReactNode
  action?: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <section className="rounded-lg border border-border bg-card p-4">
      <div className="mb-4 flex items-center justify-between">
        <h3 className="flex items-center gap-2 text-sm font-semibold">
          {icon}{title}
        </h3>
        {action}
      </div>
      {children}
    </section>
  )
}

function relativeTime(iso: string): string {
  const ms = Date.now() - new Date(iso).getTime()
  if (ms < 60_000) return 'just now'
  if (ms < 3_600_000) return `${Math.floor(ms / 60_000)} min ago`
  if (ms < 86_400_000) return `${Math.floor(ms / 3_600_000)} hr ago`
  return `${Math.floor(ms / 86_400_000)} d ago`
}

// parseUA produces a friendly"Chrome on Windows" /"Safari on iPhone"
// label from a User-Agent string. Best-effort; falls back to the raw
// UA when the heuristics don't match.
function parseUA(ua: string): string {
  if (!ua) return 'Unknown device'
  let browser = 'Browser'
  if (/Edg\//.test(ua)) browser = 'Edge'
  else if (/Chrome\//.test(ua) && !/Edg\//.test(ua)) browser = 'Chrome'
  else if (/Firefox\//.test(ua)) browser = 'Firefox'
  else if (/Safari\//.test(ua) && !/Chrome\//.test(ua)) browser = 'Safari'

  let os = 'device'
  if (/Windows/.test(ua)) os = 'Windows'
  else if (/Mac OS X/.test(ua)) os = 'macOS'
  else if (/iPhone/.test(ua)) os = 'iPhone'
  else if (/iPad/.test(ua)) os = 'iPad'
  else if (/Android/.test(ua)) os = 'Android'
  else if (/Linux/.test(ua)) os = 'Linux'
  return `${browser} on ${os}`
}

export const Route = createFileRoute('/_authenticated/settings/security')({
  component: SettingsPage,
})
