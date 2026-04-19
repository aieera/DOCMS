import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { Plus } from 'lucide-react'

import { getUsers, inviteUser } from '@/api/admin'
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

// RFC 5321 practical cap (email chars). Upper bound; server does real validation.
const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

function UsersPage() {
  const qc = useQueryClient()
  const { data, isLoading } = useQuery({
    queryKey: ['admin', 'users'],
    queryFn: () => getUsers(),
  })

  const [open, setOpen] = useState(false)
  const [email, setEmail] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [role, setRole] = useState('member')

  const reset = () => {
    setEmail('')
    setDisplayName('')
    setRole('member')
  }

  const invite = useMutation({
    mutationFn: () => inviteUser(email.trim(), role, displayName.trim() || email.trim()),
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
          <Button onClick={() => setOpen(true)} aria-label="Invite user">
            <Plus className="h-4 w-4" /> Invite User
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
            value={role}
            onValueChange={setRole}
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
