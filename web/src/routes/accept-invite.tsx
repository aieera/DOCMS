import { createFileRoute, Link, useNavigate, useSearch } from '@tanstack/react-router'
import { useState } from 'react'
import toast from 'react-hot-toast'

import { acceptInvite } from '@/api/auth'
import { Button } from '@/components/ui/Button'
import { Input } from '@/components/ui/Input'

interface InviteSearch {
  tenant?: string
  token?: string
}

function AcceptInvitePage() {
  const search = useSearch({ from: '/accept-invite' }) as InviteSearch
  const navigate = useNavigate()
  const tenantSlug = (search.tenant ?? '').trim()
  const token = (search.token ?? '').trim()

  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [loading, setLoading] = useState(false)
  const [done, setDone] = useState(false)

  const linkOK = tenantSlug !== '' && token !== ''
  const passwordOK = password.length >= 12 && password === confirm

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!linkOK || !passwordOK) return
    setLoading(true)
    try {
      await acceptInvite(tenantSlug, token, password)
      setDone(true)
      toast.success('Password set — sign in to continue')
      setTimeout(() => navigate({ to: '/login' }), 1500)
    } catch (err) {
      const m = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
      toast.error(m ?? 'Activation failed — link may be expired or already used')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-[var(--color-bg)]">
      <div className="w-full max-w-sm space-y-6 rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-8 shadow-lg">
        <div className="text-center">
          <h1 className="text-2xl font-bold text-[var(--color-primary)]">VaultDMS</h1>
          <p className="mt-1 text-sm text-[var(--color-text-secondary)]">Set your password</p>
        </div>

        {!linkOK ? (
          <p className="text-center text-sm text-red-500">
            This activation link is missing required parameters. Ask your administrator to resend.
          </p>
        ) : done ? (
          <p className="text-center text-sm text-[var(--color-text-secondary)]">
            All set — redirecting you to sign in…
          </p>
        ) : (
          <form onSubmit={handleSubmit} className="space-y-4">
            <div className="rounded bg-[var(--color-bg)] px-3 py-2 text-xs text-[var(--color-text-secondary)]">
              Tenant: <span className="font-mono">{tenantSlug}</span>
            </div>
            <Input
              label="New password"
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              placeholder="≥12 chars, mix of upper/lower/digit/special"
              required
              autoFocus
            />
            <Input
              label="Confirm password"
              type="password"
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
              required
            />
            <Button type="submit" className="w-full" loading={loading} disabled={!passwordOK}>
              Activate account
            </Button>
          </form>
        )}

        <p className="text-center text-sm text-[var(--color-text-secondary)]">
          Already activated?{' '}
          <Link to="/login" className="text-[var(--color-primary)] hover:underline">
            Sign in
          </Link>
        </p>
      </div>
    </div>
  )
}

export const Route = createFileRoute('/accept-invite')({
  component: AcceptInvitePage,
  validateSearch: (s: Record<string, unknown>): InviteSearch => ({
    tenant: typeof s.tenant === 'string' ? s.tenant : undefined,
    token: typeof s.token === 'string' ? s.token : undefined,
  }),
})
