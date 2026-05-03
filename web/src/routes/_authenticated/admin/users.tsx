import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { Copy, Plus } from 'lucide-react'

import { createUser, getUsers, inviteUser, type InviteUserResponse } from '@/api/admin'
import { PageHeader } from '@/components/shared/PageHeader'
import { UserTable } from '@/components/admin/UserTable'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { Input } from '@/components/ui/Input'
import { Select } from '@/components/ui/Select'
import { Skeleton } from '@/components/ui/Skeleton'

const ROLE_OPTIONS = [
  { value: 'member', label: 'Member' },
  { value: 'admin', label: 'Admin' },
  { value: 'owner', label: 'Owner' },
]

const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

type Mode = 'invite' | 'direct'

function buildActivationURL(slug: string, token: string): string {
  return `${window.location.origin}/accept-invite?tenant=${encodeURIComponent(slug)}&token=${encodeURIComponent(token)}`
}

function UsersPage() {
  const qc = useQueryClient()
  const { data, isLoading } = useQuery({
    queryKey: ['admin', 'users'],
    queryFn: () => getUsers(),
  })

  const [open, setOpen] = useState(false)
  const [mode, setMode] = useState<Mode>('invite')
  const [email, setEmail] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [role, setRole] = useState('member')
  const [password, setPassword] = useState('')
  const [issued, setIssued] = useState<InviteUserResponse | null>(null)

  const reset = () => {
    setEmail('')
    setDisplayName('')
    setRole('member')
    setPassword('')
    setIssued(null)
    setMode('invite')
  }

  const invite = useMutation({
    mutationFn: () => inviteUser(email.trim(), role, displayName.trim() || email.trim()),
    onSuccess: (resp) => {
      qc.invalidateQueries({ queryKey: ['admin', 'users'] })
      setIssued(resp)
      toast.success(`Invitation created for ${email.trim()}`)
    },
    onError: (err: unknown) => toast.error(messageFrom(err) ?? 'Failed to send invitation'),
  })

  const create = useMutation({
    mutationFn: () =>
      createUser(email.trim(), password, displayName.trim() || email.trim(), role),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['admin', 'users'] })
      toast.success(`User ${email.trim()} created`)
      setOpen(false)
      reset()
    },
    onError: (err: unknown) => toast.error(messageFrom(err) ?? 'Failed to create user'),
  })

  const emailOk = EMAIL_RE.test(email.trim())
  // Server enforces 12..128 + complexity. Mirror the length floor here so
  // the button enables predictably.
  const passwordOk = password.length >= 12 && password.length <= 128
  const busy = invite.isPending || create.isPending
  const canSubmit =
    !busy && emailOk && (mode === 'invite' || passwordOk)

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    if (!canSubmit) return
    if (mode === 'invite') invite.mutate()
    else create.mutate()
  }

  return (
    <div>
      <PageHeader
        title="Users"
        description="Manage team members"
        actions={
          <Button onClick={() => setOpen(true)} aria-label="Add user">
            <Plus className="h-4 w-4" /> Add User
          </Button>
        }
      />

      {isLoading ? (
        <Skeleton className="h-64" />
      ) : (
        <UserTable users={data?.items || []} />
      )}

      <Dialog
        open={open}
        onOpenChange={(v) => {
          setOpen(v)
          if (!v) reset()
        }}
        title={issued ? 'Invitation ready' : 'Add a new user'}
        description={
          issued
            ? 'Send the activation link below — the invitee uses it to set their password (valid 72 h).'
            : mode === 'invite'
              ? 'They receive a one-time link to set their own password.'
              : 'You set their initial password directly. They can sign in immediately.'
        }
      >
        {issued ? (
          <ActivationLinkPanel
            issued={issued}
            onClose={() => {
              setOpen(false)
              reset()
            }}
          />
        ) : (
          <form className="flex flex-col gap-4" onSubmit={onSubmit}>
            <ModeTabs mode={mode} onChange={setMode} disabled={busy} />

            <Input
              label="Email"
              type="email"
              placeholder="colleague@example.com"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              required
              autoFocus
            />
            <Input
              label="Display name (optional)"
              placeholder="Jane Doe"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
            />
            <Select
              label="Role"
              value={role}
              onValueChange={setRole}
              options={ROLE_OPTIONS}
            />
            {mode === 'direct' && (
              <Input
                label="Initial password"
                type="password"
                placeholder="≥12 chars, mix of upper/lower/digit/special"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
              />
            )}

            <div className="mt-2 flex justify-end gap-2">
              <Button type="button" variant="ghost" onClick={() => setOpen(false)} disabled={busy}>
                Cancel
              </Button>
              <Button type="submit" disabled={!canSubmit}>
                {busy
                  ? mode === 'invite'
                    ? 'Sending…'
                    : 'Creating…'
                  : mode === 'invite'
                    ? 'Send invitation'
                    : 'Create user'}
              </Button>
            </div>
          </form>
        )}
      </Dialog>
    </div>
  )
}

function ModeTabs({
  mode,
  onChange,
  disabled,
}: {
  mode: Mode
  onChange: (m: Mode) => void
  disabled: boolean
}) {
  const opts: { value: Mode; label: string }[] = [
    { value: 'invite', label: 'Invite by email' },
    { value: 'direct', label: 'Create with password' },
  ]
  return (
    <div className="flex rounded border border-[var(--color-border)] p-0.5 text-xs">
      {opts.map((o) => (
        <button
          key={o.value}
          type="button"
          disabled={disabled}
          onClick={() => onChange(o.value)}
          className={`flex-1 rounded px-3 py-1.5 transition-colors ${
            mode === o.value
              ? 'bg-[var(--color-primary)] text-white'
              : 'text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-secondary)]'
          }`}
        >
          {o.label}
        </button>
      ))}
    </div>
  )
}

function ActivationLinkPanel({
  issued,
  onClose,
}: {
  issued: InviteUserResponse
  onClose: () => void
}) {
  const url = buildActivationURL(issued.tenant_slug, issued.invite_token)
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(url)
      toast.success('Link copied')
    } catch {
      toast.error('Copy failed — select and copy manually')
    }
  }
  return (
    <div className="flex flex-col gap-4">
      <div className="rounded border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-3">
        <div className="text-xs uppercase tracking-wide text-[var(--color-text-secondary)]">
          Activation URL for {issued.user.email}
        </div>
        <div className="mt-2 flex items-center gap-2">
          <code className="flex-1 truncate rounded bg-[var(--color-bg)] px-2 py-1 text-xs">
            {url}
          </code>
          <Button type="button" size="sm" variant="outline" onClick={copy} aria-label="Copy link">
            <Copy className="h-3 w-3" />
          </Button>
        </div>
      </div>
      <p className="text-xs text-[var(--color-text-secondary)]">
        SMTP isn't wired in this environment — share this link directly. The token is valid for 72 hours
        and can only be used once.
      </p>
      <div className="flex justify-end">
        <Button type="button" onClick={onClose}>Done</Button>
      </div>
    </div>
  )
}

function messageFrom(err: unknown): string | undefined {
  const e = err as { response?: { data?: { message?: string } }; message?: string }
  return e?.response?.data?.message ?? e?.message
}

export const Route = createFileRoute('/_authenticated/admin/users')({ component: UsersPage })
