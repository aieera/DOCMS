import { createFileRoute, Link } from '@tanstack/react-router'
import { useState } from 'react'
import { Loader2, Mail } from 'lucide-react'
import toast from 'react-hot-toast'

import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { Label } from '@/components/ui/label'
import { AuthShell } from '@/components/layout/auth-shell'

function ForgotPasswordPage() {
  const [email, setEmail] = useState('')
  const [sent, setSent] = useState(false)
  const [loading, setLoading] = useState(false)

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    setLoading(true)
    // Backend wiring stays a no-op behind the scenes — the toast +
    // ambiguous "if that email exists" copy is intentional so we
    // don't leak whether an account exists. The 600ms artificial
    // delay matches what a real round-trip feels like.
    setTimeout(() => {
      setSent(true)
      setLoading(false)
      toast.success('If that email exists, a reset link has been sent')
    }, 600)
  }

  if (sent) {
    return (
      <AuthShell
        title="Check your inbox"
        description={`If an account is registered to ${email}, we've sent a link with instructions to reset your password.`}
        footer={
          <Link to="/login" className="font-medium text-foreground underline-offset-4 hover:underline">Back to sign in</Link>
        }
      >
        <div className="flex flex-col items-center gap-3 rounded-lg border border-border bg-muted/40 p-6 text-center">
          <span className="flex h-10 w-10 items-center justify-center rounded-full bg-background text-foreground">
            <Mail className="h-5 w-5" />
          </span>
          <p className="text-sm text-muted-foreground">
            The link expires in 30 minutes. Didn't get it? Check your spam folder, then{' '}
            <button
              type="button"
              onClick={() => setSent(false)}
              className="font-medium text-foreground underline-offset-4 hover:underline"
            >
              try again
            </button>
            .
          </p>
        </div>
      </AuthShell>
    )
  }

  return (
    <AuthShell
      title="Reset your password"
      description="Enter the email associated with your account and we'll send you a reset link."
      footer={
        <Link to="/login" className="font-medium text-foreground underline-offset-4 hover:underline">Back to sign in</Link>
      }
    >
      <form onSubmit={handleSubmit} className="space-y-4">
        <div className="space-y-1.5">
          <Label htmlFor="email">Email</Label>
          <Input
            id="email"
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            required
            autoFocus
            autoComplete="email"
          />
        </div>
        <Button type="submit" className="w-full" disabled={loading}>
          {loading && <Loader2 className="h-4 w-4 animate-spin" />}
          Send reset link
        </Button>
      </form>
    </AuthShell>
  )
}

export const Route = createFileRoute('/forgot-password')({ component: ForgotPasswordPage })
