import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { Plus, RefreshCw } from 'lucide-react'

import {
  forcePasswordReset,
  getUsers,
  inviteUser,
  resetMFA,
  suspendUser,
  sweepExpiredPasswords,
} from '@/api/admin'
import { PageHeader } from '@/components/shared/PageHeader'
import { UserTable } from '@/components/admin/UserTable'
import { Button } from '@/components/ui/Button'
import { ConfirmDialog } from '@/components/ui/ConfirmDialog'
import { Dialog } from '@/components/ui/Dialog'
import { Input } from '@/components/ui/Input'
import { Select } from '@/components/ui/Select'
import { Skeleton } from '@/components/ui/Skeleton'
import { useAuthStore } from '@/store/authStore'
import type { User } from '@/types/api'

const ROLE_OPTIONS = [
  { value: 'member', label: 'Member' },
  { value: 'admin', label: 'Admin' },
  { value: 'owner', label: 'Owner' },
]

// RFC 5321 practical cap (email chars). Upper bound; server does real validation.
const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

function UsersPage() {
  const qc = useQueryClient()
  const role = useAuthStore((s) => s.user?.role)
  const isOwner = role === 'owner'

  const { data, isLoading } = useQuery({
    queryKey: ['admin', 'users'],
    queryFn: () => getUsers(),
  })

  const [open, setOpen] = useState(false)
  const [email, setEmail] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [inviteRole, setInviteRole] = useState('member')

  // Per-row confirm targets. We use ConfirmDialog rather than window.confirm
  // so the audit-trail copy is visible — admins should know each click lands
  // in the audit log.
  const [resetMFATarget, setResetMFATarget] = useState<User | null>(null)
  const [forceResetTarget, setForceResetTarget] = useState<User | null>(null)
  const [suspendTarget, setSuspendTarget] = useState<User | null>(null)
  const [sweepOpen, setSweepOpen] = useState(false)

  const onMutationError = (verb: string) => (err: unknown) => {
    const anyErr = err as { response?: { data?: { message?: string } }; message?: string }
    toast.error(anyErr?.response?.data?.message ?? anyErr?.message ?? `Failed to ${verb}`)
  }

  const resetMFAMut = useMutation({
    mutationFn: (id: string) => resetMFA(id),
    onSuccess: (_, id) => {
      const u = data?.items.find((x) => x.id === id)
      toast.success(`MFA reset for ${u?.display_name ?? 'user'}`)
      qc.invalidateQueries({ queryKey: ['admin', 'users'] })
      setResetMFATarget(null)
    },
    onError: onMutationError('reset MFA'),
  })

  const forceResetMut = useMutation({
    mutationFn: (id: string) => forcePasswordReset(id),
    onSuccess: (_, id) => {
      const u = data?.items.find((x) => x.id === id)
      toast.success(`${u?.display_name ?? 'User'} will be required to change password on next login`)
      qc.invalidateQueries({ queryKey: ['admin', 'users'] })
      setForceResetTarget(null)
    },
    onError: onMutationError('force password reset'),
  })

  const suspendMut = useMutation({
    mutationFn: (id: string) => suspendUser(id),
    onSuccess: (_, id) => {
      const u = data?.items.find((x) => x.id === id)
      toast.success(`${u?.display_name ?? 'User'} suspended`)
      qc.invalidateQueries({ queryKey: ['admin', 'users'] })
      setSuspendTarget(null)
    },
    onError: onMutationError('suspend user'),
  })

  const sweepMut = useMutation({
    mutationFn: () => sweepExpiredPasswords(),
    onSuccess: (r) => {
      toast.success(
        r.swept === 0
          ? 'No users had passwords past the expiry threshold.'
          : `Flagged ${r.swept} user${r.swept === 1 ? '' : 's'} for password change on next login.`,
      )
      qc.invalidateQueries({ queryKey: ['admin', 'users'] })
      setSweepOpen(false)
    },
    onError: onMutationError('sweep expired passwords'),
  })

  const reset = () => {
    setEmail('')
    setDisplayName('')
    setInviteRole('member')
  }

  const invite = useMutation({
    mutationFn: () => inviteUser(email.trim(), inviteRole, displayName.trim() || email.trim()),
    onSuccess: () => {
      toast.success(`Invitation sent to ${email.trim()}`)
      qc.invalidateQueries({ queryKey: ['admin', 'users'] })
      setOpen(false)
      reset()
    },
    onError: (err: unknown) => {
      // Axios error shape: error.response.data.message
      const anyErr = err as { response?: { data?: { message?: string } }; message?: string }
      toast.error(
        anyErr?.response?.data?.message ??
          anyErr?.message ??
          'Failed to send invitation',
      )
    },
  })

  const canSubmit = EMAIL_RE.test(email.trim()) && !invite.isPending

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    if (!canSubmit) return
    invite.mutate()
  }

  return (
    <div>
      <PageHeader
        title="Users"
        description="Manage team members"
        actions={
          <div className="flex items-center gap-2">
            {isOwner && (
              <Button
                variant="outline"
                onClick={() => setSweepOpen(true)}
                aria-label="Sweep expired passwords"
                title="Flag every user past the password-expiry threshold for must-change-password on next login"
              >
                <RefreshCw className="h-4 w-4" /> Sweep expired passwords
              </Button>
            )}
            <Button onClick={() => setOpen(true)} aria-label="Invite user">
              <Plus className="h-4 w-4" /> Invite User
            </Button>
          </div>
        }
      />

      {isLoading ? (
        <Skeleton className="h-64" />
      ) : (
        <UserTable
          users={data?.items || []}
          actions={{
            onResetMFA: setResetMFATarget,
            onForcePasswordReset: setForceResetTarget,
            onSuspend: setSuspendTarget,
          }}
        />
      )}

      <ConfirmDialog
        open={resetMFATarget !== null}
        onOpenChange={(v) => !v && setResetMFATarget(null)}
        title="Reset MFA?"
        description={
          resetMFATarget
            ? `${resetMFATarget.display_name} will lose their authenticator and recovery codes. They'll be prompted to enroll again on next sign-in. The reset is recorded in the audit trail.`
            : ''
        }
        confirmLabel="Reset MFA"
        loading={resetMFAMut.isPending}
        onConfirm={() => resetMFATarget && resetMFAMut.mutate(resetMFATarget.id)}
      />

      <ConfirmDialog
        open={forceResetTarget !== null}
        onOpenChange={(v) => !v && setForceResetTarget(null)}
        title="Force password reset?"
        description={
          forceResetTarget
            ? `${forceResetTarget.display_name} will be required to set a new password on next login. Active sessions are not affected. Use this when a credential is suspected leaked.`
            : ''
        }
        confirmLabel="Force reset"
        loading={forceResetMut.isPending}
        onConfirm={() => forceResetTarget && forceResetMut.mutate(forceResetTarget.id)}
      />

      <ConfirmDialog
        open={suspendTarget !== null}
        onOpenChange={(v) => !v && setSuspendTarget(null)}
        title="Suspend user?"
        description={
          suspendTarget
            ? `${suspendTarget.display_name} will be signed out of all sessions and blocked from logging in. Their data is retained.`
            : ''
        }
        confirmLabel="Suspend"
        destructive
        loading={suspendMut.isPending}
        onConfirm={() => suspendTarget && suspendMut.mutate(suspendTarget.id)}
      />

      <ConfirmDialog
        open={sweepOpen}
        onOpenChange={setSweepOpen}
        title="Sweep expired passwords?"
        description="Every user whose password has crossed this tenant's expiry threshold will be flagged must-change-password on next login. Active sessions remain valid until they expire. Owner-only action; recorded in the audit trail."
        confirmLabel="Run sweep"
        loading={sweepMut.isPending}
        onConfirm={() => sweepMut.mutate()}
      />

      <Dialog
        open={open}
        onOpenChange={(v) => {
          setOpen(v)
          if (!v) reset()
        }}
        title="Invite a new user"
        description="They will receive an email with a link to set their password."
      >
        <form className="flex flex-col gap-4" onSubmit={onSubmit}>
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
            value={inviteRole}
            onValueChange={setInviteRole}
            options={ROLE_OPTIONS}
          />

          <div className="mt-2 flex justify-end gap-2">
            <Button
              type="button"
              variant="ghost"
              onClick={() => setOpen(false)}
              disabled={invite.isPending}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={!canSubmit}>
              {invite.isPending ? 'Sending…' : 'Send invitation'}
            </Button>
          </div>
        </form>
      </Dialog>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/users')({ component: UsersPage })
