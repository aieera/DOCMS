import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useMemo, useState } from 'react'
import toast from 'react-hot-toast'

import { changePassword } from '@/api/auth'
import { Button } from '@/components/ui/Button'
import { Input } from '@/components/ui/Input'
import { useAuthStore } from '@/store/authStore'

type Search = { token?: string }

type Check = { label: string; ok: boolean }

function scoreRules(pw: string): Check[] {
  return [
    { label: '12–128 characters', ok: pw.length >= 12 && pw.length <= 128 },
    { label: 'Uppercase + lowercase', ok: /[A-Z]/.test(pw) && /[a-z]/.test(pw) },
    { label: 'Digit', ok: /\d/.test(pw) },
    { label: 'Special character (! @ # $ % ^ & *)', ok: /[!@#$%^&*]/.test(pw) },
  ]
}

function ChangePasswordPage() {
  const { token } = Route.useSearch()
  const navigate = useNavigate()
  const authLogin = useAuthStore((s) => s.login)

  const [pw, setPw] = useState('')
  const [confirm, setConfirm] = useState('')
  const [show, setShow] = useState(false)
  const [loading, setLoading] = useState(false)

  const checks = useMemo(() => scoreRules(pw), [pw])
  const allOk = checks.every((c) => c.ok) && pw === confirm

  if (!token) {
    return (
      <div className="flex min-h-screen items-center justify-center">
        <div className="max-w-sm rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-8 text-center">
          <h1 className="text-lg font-semibold">Change-password link expired</h1>
          <p className="mt-2 text-sm text-[var(--color-text-secondary)]">
            Sign in again to receive a new one.
          </p>
          <Button className="mt-4 w-full" onClick={() => navigate({ to: '/login' })}>
            Back to sign in
          </Button>
        </div>
      </div>
    )
  }

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!allOk) return
    setLoading(true)
    try {
      const data = await changePassword(token, pw)
      // Session is now established via cookie + server-set CSRF.
      const user = data.user as unknown as { tenant_id?: string } & Record<string, unknown>
      authLogin(user as never, user.tenant_id ?? '')
      toast.success('Password updated.')
      navigate({ to: '/' })
    } catch (err: unknown) {
      const msg =
        err && typeof err === 'object' && 'response' in err
          ? // eslint-disable-next-line @typescript-eslint/no-explicit-any
            (err as any).response?.data?.error?.message ?? 'Could not update password'
          : 'Could not update password'
      toast.error(msg)
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-[var(--color-bg)]">
      <div className="w-full max-w-md space-y-6 rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-8 shadow-lg">
        <div className="text-center">
          <h1 className="text-2xl font-bold text-[var(--color-primary)]">Set a new password</h1>
          <p className="mt-1 text-sm text-[var(--color-text-secondary)]">
            Your administrator requires a password change before continuing.
          </p>
        </div>

        <form onSubmit={submit} className="space-y-4" noValidate>
          <div className="space-y-1">
            <Input
              label="New password"
              type={show ? 'text' : 'password'}
              autoComplete="new-password"
              value={pw}
              onChange={(e) => setPw(e.target.value)}
              required
              autoFocus
            />
            <button
              type="button"
              onClick={() => setShow((s) => !s)}
              className="text-xs text-[var(--color-primary)] hover:underline"
              aria-pressed={show}
            >
              {show ? 'Hide passwords' : 'Show passwords'}
            </button>
          </div>
          <Input
            label="Confirm new password"
            type={show ? 'text' : 'password'}
            autoComplete="new-password"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            required
          />

          <ul className="space-y-1 rounded-md bg-[var(--color-bg)] p-3 text-xs" aria-label="Password requirements">
            {checks.map((c) => (
              <li
                key={c.label}
                className={c.ok ? 'text-green-600' : 'text-[var(--color-text-secondary)]'}
              >
                <span aria-hidden="true">{c.ok ? '✓' : '·'}</span> {c.label}
              </li>
            ))}
            <li className={pw && pw === confirm ? 'text-green-600' : 'text-[var(--color-text-secondary)]'}>
              <span aria-hidden="true">{pw && pw === confirm ? '✓' : '·'}</span> Confirmation matches
            </li>
          </ul>

          <Button type="submit" className="w-full" loading={loading} disabled={!allOk}>
            Update password & sign in
          </Button>
        </form>
      </div>
    </div>
  )
}

export const Route = createFileRoute('/change-password')({
  validateSearch: (search: Record<string, unknown>): Search => ({
    token: typeof search.token === 'string' ? search.token : undefined,
  }),
  component: ChangePasswordPage,
})
