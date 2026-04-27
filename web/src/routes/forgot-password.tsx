import { createFileRoute, Link } from '@tanstack/react-router'
import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'

import { forgotPassword } from '@/api/auth'
import { Button } from '@/components/ui/Button'
import { Input } from '@/components/ui/Input'

function ForgotPasswordPage() {
  const [tenantSlug, setTenantSlug] = useState('')
  const [email, setEmail] = useState('')
  const [submitted, setSubmitted] = useState(false)

  const submit = useMutation({
    mutationFn: () => forgotPassword(tenantSlug.trim().toLowerCase(), email.trim().toLowerCase()),
    onSuccess: () => setSubmitted(true),
    // ADR-style no-oracle: even on network/backend error, behave the
    // same. The user sees the same confirmation regardless of whether
    // their email exists in the system.
    onError: () => setSubmitted(true),
  })

  const valid = tenantSlug.trim() && /\S+@\S+\.\S+/.test(email)

  return (
    <div className="flex min-h-screen items-center justify-center bg-[var(--color-bg)]">
      <div className="w-full max-w-sm space-y-6 rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-8 shadow-lg">
        <div className="text-center">
          <h1 className="text-2xl font-bold text-[var(--color-primary)]">VaultDMS</h1>
          <p className="mt-1 text-sm text-[var(--color-text-secondary)]">Reset your password</p>
        </div>
        {submitted ? (
          <div className="space-y-3 text-sm">
            <p className="text-[var(--color-text-secondary)]">
              If your email matches a record in our system you will receive a reset link
              within a few minutes. The link is valid for 10 minutes.
            </p>
            <p className="text-xs text-[var(--color-text-secondary)]">
              SSO-federated accounts can't be reset here — sign in via your identity provider.
            </p>
          </div>
        ) : (
          <form
            onSubmit={(e) => {
              e.preventDefault()
              if (valid && !submit.isPending) submit.mutate()
            }}
            className="space-y-4"
            noValidate
          >
            <Input
              label="Organisation slug"
              value={tenantSlug}
              onChange={(e) => setTenantSlug(e.target.value)}
              placeholder="acme"
              autoFocus
              required
              data-testid="forgot-tenant-slug"
            />
            <Input
              label="Email"
              type="email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              placeholder="you@example.com"
              required
              data-testid="forgot-email"
            />
            <Button
              type="submit"
              className="w-full"
              loading={submit.isPending}
              disabled={!valid}
              data-testid="forgot-submit"
            >
              Send reset link
            </Button>
          </form>
        )}
        <p className="text-center text-sm">
          <Link to="/login" className="text-[var(--color-primary)] hover:underline">
            Back to sign in
          </Link>
        </p>
      </div>
    </div>
  )
}

export const Route = createFileRoute('/forgot-password')({ component: ForgotPasswordPage })
