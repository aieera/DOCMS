// Public, unauthenticated DSR intake form (ADR 0037).
//
// Reachable at /dsr-request. The form posts to /api/v1/dsr/intake;
// regardless of whether the email matches a real subject in the
// system, we always return a generic accepted-202 (the no-oracle
// rule from ADR 0037). Subjects find out their request was real
// when they receive the verification email; spammers see exactly
// the same response.

import { createFileRoute, Link } from '@tanstack/react-router'
import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { Shield, Check } from 'lucide-react'

import { publicIntake, type DSRIntakeBody } from '@/api/privacy'
import { Button } from '@/components/ui/Button'
import { Input } from '@/components/ui/Input'
import { Textarea } from '@/components/ui/Textarea'

const REQUEST_TYPES: { value: DSRIntakeBody['request_type']; label: string; description: string }[] = [
  { value: 'access', label: 'Access (Art. 15)', description: 'Get a copy of the personal data we hold about you.' },
  { value: 'portability', label: 'Portability (Art. 20)', description: 'Same as access, in a machine-readable format you can move elsewhere.' },
  { value: 'rectification', label: 'Rectification (Art. 16)', description: 'Correct or update inaccurate data we hold about you.' },
  { value: 'erasure', label: 'Erasure (Art. 17)', description: 'Delete your personal data. Subject to legal-hold and regulatory retention exceptions.' },
]

function DSRRequestPage() {
  const [tenantSlug, setTenantSlug] = useState('')
  const [email, setEmail] = useState('')
  const [requestType, setRequestType] = useState<DSRIntakeBody['request_type']>('access')
  const [description, setDescription] = useState('')
  const [submittedAt, setSubmittedAt] = useState<Date | null>(null)

  const submit = useMutation({
    mutationFn: () => publicIntake({
      tenant_slug: tenantSlug.trim().toLowerCase(),
      requester_email: email.trim().toLowerCase(),
      request_type: requestType,
      description: description.trim(),
    }),
    onSuccess: () => {
      setSubmittedAt(new Date())
    },
    onError: () => {
      // Don't surface backend internals to the public caller. Even
      // a 5xx looks the same as a 422 from here — see ADR 0037
      // §"What we did not do" oracle defense.
      toast.error('Could not submit. Please try again or contact your data protection officer directly.')
    },
  })

  if (submittedAt) {
    return (
      <Layout>
        <div className="text-center">
          <div className="mx-auto mb-4 flex h-12 w-12 items-center justify-center rounded-full bg-emerald-100 dark:bg-emerald-950/40">
            <Check className="h-6 w-6 text-emerald-700 dark:text-emerald-300" />
          </div>
          <h2 className="text-lg font-semibold">Request received</h2>
          <p className="mt-2 text-sm text-[var(--color-text-secondary)]">
            If your email matches a record in our system you will receive a
            verification email within a few minutes. The link in that email is
            valid for 7 days.
          </p>
          <p className="mt-4 text-xs text-[var(--color-text-secondary)]">
            Didn't receive it? Check your spam folder, then contact your
            organisation's data protection officer.
          </p>
        </div>
      </Layout>
    )
  }

  const valid = tenantSlug.trim() && /\S+@\S+\.\S+/.test(email)

  return (
    <Layout>
      <div className="mb-4 flex items-center gap-2">
        <Shield className="h-5 w-5 text-[var(--color-primary)]" />
        <h2 className="text-lg font-semibold">Submit a data subject request</h2>
      </div>
      <p className="mb-4 text-sm text-[var(--color-text-secondary)]">
        Use this form to exercise your rights under GDPR Articles 15, 16, 17, and 20.
        We'll send a verification email to confirm the request is from you.
      </p>
      <form
        onSubmit={(e) => {
          e.preventDefault()
          if (valid && !submit.isPending) submit.mutate()
        }}
        className="space-y-4"
      >
        <Input
          label="Organisation slug"
          value={tenantSlug}
          onChange={(e) => setTenantSlug(e.target.value)}
          placeholder="acme"
          autoFocus
          required
          data-testid="dsr-tenant-slug"
        />
        <Input
          label="Your email address"
          type="email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          placeholder="you@example.com"
          required
          data-testid="dsr-email"
        />
        <div>
          <label className="mb-1 block text-xs font-medium">Request type</label>
          <div className="space-y-2">
            {REQUEST_TYPES.map((opt) => (
              <label
                key={opt.value}
                className={`flex cursor-pointer items-start gap-2 rounded border p-2 text-sm ${
                  requestType === opt.value
                    ? 'border-[var(--color-primary)] bg-[var(--color-bg)]'
                    : 'border-[var(--color-border)]'
                }`}
              >
                <input
                  type="radio"
                  name="request_type"
                  value={opt.value}
                  checked={requestType === opt.value}
                  onChange={() => setRequestType(opt.value)}
                  className="mt-0.5"
                  data-testid={`dsr-type-${opt.value}`}
                />
                <span>
                  <strong>{opt.label}</strong>
                  <span className="block text-xs text-[var(--color-text-secondary)]">
                    {opt.description}
                  </span>
                </span>
              </label>
            ))}
          </div>
        </div>
        <Textarea
          label="Description (optional)"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          rows={4}
          placeholder="Anything that helps us locate your records — past employer, account name, etc."
        />
        <Button
          type="submit"
          disabled={!valid || submit.isPending}
          loading={submit.isPending}
          className="w-full"
          data-testid="dsr-submit"
        >
          Submit request
        </Button>
      </form>
      <p className="mt-4 text-xs text-[var(--color-text-secondary)]">
        Already submitted?{' '}
        <Link to="/dsr-status" className="text-[var(--color-primary)] hover:underline">
          Check the status of your request →
        </Link>
      </p>
    </Layout>
  )
}

function Layout({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex min-h-screen items-center justify-center bg-[var(--color-bg)] px-4 py-8">
      <div className="w-full max-w-md rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-8 shadow-lg">
        <div className="mb-6 text-center">
          <h1 className="text-2xl font-bold text-[var(--color-primary)]">VaultDMS</h1>
          <p className="text-xs text-[var(--color-text-secondary)]">Data subject rights · GDPR</p>
        </div>
        {children}
      </div>
    </div>
  )
}

export const Route = createFileRoute('/dsr-request')({ component: DSRRequestPage })
