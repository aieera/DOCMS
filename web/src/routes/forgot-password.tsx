import { createFileRoute, Link } from '@tanstack/react-router'
import { useState } from 'react'
import { Button } from '@/components/ui/Button'
import { Input } from '@/components/ui/Input'
import toast from 'react-hot-toast'

function ForgotPasswordPage() {
  const [email, setEmail] = useState('')
  const [sent, setSent] = useState(false)

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    setSent(true)
    toast.success('If that email exists, a reset link has been sent')
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-[var(--color-bg)]">
      <div className="w-full max-w-sm space-y-6 rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-8 shadow-lg">
        <div className="text-center">
          <h1 className="text-2xl font-bold text-[var(--color-primary)]">VaultDMS</h1>
          <p className="mt-1 text-sm text-[var(--color-text-secondary)]">Reset your password</p>
        </div>
        {sent ? (
          <p className="text-center text-sm text-[var(--color-text-secondary)]">Check your email for a reset link.</p>
        ) : (
          <form onSubmit={handleSubmit} className="space-y-4">
            <Input label="Email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} required autoFocus />
            <Button type="submit" className="w-full">Send Reset Link</Button>
          </form>
        )}
        <p className="text-center text-sm"><Link to="/login" className="text-[var(--color-primary)] hover:underline">Back to login</Link></p>
      </div>
    </div>
  )
}

export const Route = createFileRoute('/forgot-password')({ component: ForgotPasswordPage })
