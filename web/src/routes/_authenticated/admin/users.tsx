import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Copy, Plus } from 'lucide-react'

import { createUser, getUsers, inviteUser, type InviteUserResponse } from '@/api/admin'
import { PageHeader } from '@/components/shared/PageHeader'
import { UserTable } from '@/components/admin/UserTable'
import { Button } from '@/components/ui/shadcn/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/shadcn/dialog'
import { Input } from '@/components/ui/shadcn/input'
import { LabeledSelect as Select } from '@/components/ui/shadcn/select'
import { Card } from '@/components/ui/card'
import { cn } from '@/lib/cn'

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
    setEmail(''); setDisplayName(''); setRole('member'); setPassword(''); setIssued(null); setMode('invite')
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
    mutationFn: () => createUser(email.trim(), password, displayName.trim() || email.trim(), role),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['admin', 'users'] })
      toast.success(`User ${email.trim()} created`)
      setOpen(false)
      reset()
    },
    onError: (err: unknown) => toast.error(messageFrom(err) ?? 'Failed to create user'),
  })

  const emailOk = EMAIL_RE.test(email.trim())
  const passwordOk = password.length >= 12 && password.length <= 128
  const busy = invite.isPending || create.isPending

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    if (busy) return
    // Surface why submission is blocked instead of silently failing
    // — empty + invalid email both used to render as a no-op click.
    if (email.trim() === '') {
      toast.error('Email is required')
      return
    }
    if (!emailOk) {
      toast.error('Enter a valid email address')
      return
    }
    if (mode === 'direct' && !passwordOk) {
      toast.error('Password must be 12–128 characters')
      return
    }
    if (mode === 'invite') invite.mutate()
    else create.mutate()
  }

  const users = data?.items ?? []

  return (
    <div className="space-y-6">
      <PageHeader
        title="Users"
        description="Manage team members, roles, and access. New users can be invited by email or created directly with an initial password."
        actions={
          <Button onClick={() => setOpen(true)} aria-label="Add user" data-testid="add-user">
            <Plus className="h-4 w-4" /> Add user
          </Button>
        }
      />

      {/* UserTable wraps the canonical DataTable internally — its
          isLoading + empty handling now flow through the shared
          chrome (skeleton row + "No results" text). The Card wrapper
          here just gives it a single shadowed surface. */}
      <UserTable users={users} isLoading={isLoading} />

      <Dialog open={open} onOpenChange={(v) => { setOpen(v); if (!v) reset() }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{issued ? 'Invitation ready' : 'Add a new user'}</DialogTitle>
            <DialogDescription>
              {issued
                ? 'Share the activation link below — the invitee uses it to set their password (valid 72h).'
                : mode === 'invite'
                  ? 'They receive a one-time link to set their own password.'
                  : 'You set their initial password directly. They can sign in immediately.'}
            </DialogDescription>
          </DialogHeader>
          {issued ? (
            <ActivationLinkPanel issued={issued} onClose={() => { setOpen(false); reset() }} />
          ) : (
            <form className="space-y-4" onSubmit={onSubmit}>
              <ModeTabs mode={mode} onChange={setMode} disabled={busy} />
              <Input
                label="Email"
                type="email"
                placeholder="colleague@example.com"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                required
                autoFocus
                autoComplete="email"
                error={
                  email.trim() === ''
                    ? undefined
                    : !emailOk
                      ? 'Enter a valid email address'
                      : undefined
                }
              />
              <Input
                label="Display name (optional)"
                placeholder="Jane Doe"
                value={displayName}
                onChange={(e) => setDisplayName(e.target.value)}
                autoComplete="name"
              />
              <Select label="Role" value={role} onValueChange={setRole} options={ROLE_OPTIONS} />
              {mode === 'direct' && (
                <Input
                  label="Initial password"
                  type="password"
                  placeholder="≥12 chars, mix of upper/lower/digit/special"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  required
                  autoComplete="new-password"
                  error={
                    password === ''
                      ? undefined
                      : password.length < 12
                        ? 'Must be at least 12 characters'
                        : password.length > 128
                          ? 'Must be 128 characters or fewer'
                          : undefined
                  }
                />
              )}
              <DialogFooter>
                <Button type="button" variant="ghost" onClick={() => setOpen(false)} disabled={busy}>
                  Cancel
                </Button>
                <Button type="submit" disabled={busy} loading={busy}>
                  {mode === 'invite' ? 'Send invitation' : 'Create user'}
                </Button>
              </DialogFooter>
            </form>
          )}
        </DialogContent>
      </Dialog>
    </div>
  )
}

// Two-mode segmented control. Same visual language as the document
// detail tab pill.
function ModeTabs({ mode, onChange, disabled }: { mode: Mode; onChange: (m: Mode) => void; disabled: boolean }) {
  const opts: { value: Mode; label: string }[] = [
    { value: 'invite', label: 'Invite by email' },
    { value: 'direct', label: 'Create with password' },
  ]
  return (
    <div className="inline-flex w-full gap-1 rounded-md bg-muted/60 p-1 text-xs" role="tablist">
      {opts.map((o) => {
        const active = mode === o.value
        return (
          <button
            key={o.value}
            type="button"
            role="tab"
            aria-selected={active}
            disabled={disabled}
            onClick={() => onChange(o.value)}
            className={cn(
              'flex-1 rounded-sm px-3 py-1.5 transition-all',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
              active ? 'bg-background text-foreground shadow-sm font-medium' : 'text-muted-foreground hover:text-foreground',
            )}
          >
            {o.label}
          </button>
        )
      })}
    </div>
  )
}

function ActivationLinkPanel({ issued, onClose }: { issued: InviteUserResponse; onClose: () => void }) {
  const url = buildActivationURL(issued.tenant_slug, issued.invite_token)
  const copy = async () => {
    try { await navigator.clipboard.writeText(url); toast.success('Link copied') }
    catch { toast.error('Copy failed — select and copy manually') }
  }
  return (
    <div className="space-y-4">
      <Card className="p-3">
        <p className="text-xs uppercase tracking-wider text-muted-foreground">
          Activation URL for {issued.user.email}
        </p>
        <div className="mt-2 flex items-center gap-2">
          <code className="flex-1 truncate rounded bg-muted px-2 py-1 text-xs font-mono">{url}</code>
          <Button type="button" size="sm" variant="outline" onClick={copy} aria-label="Copy link">
            <Copy className="h-3 w-3" />
          </Button>
        </div>
      </Card>
      <p className="text-xs text-muted-foreground">
        SMTP isn't wired in this environment — share this link directly. The token is valid for 72 hours and can only be used once.
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
