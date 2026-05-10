import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { toast } from 'sonner'

import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { AuthShell } from '@/components/layout/auth-shell'
import { register } from '@/api/auth'

function RegisterPage() {
  const [tenantSlug, setTenantSlug] = useState('acme')
  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [loading, setLoading] = useState(false)
  const navigate = useNavigate()

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setLoading(true)
    try {
      await register(email, password, name, tenantSlug)
      toast.success('Account created — please log in')
      setTimeout(() => navigate({ to: '/login' }), 800)
    } catch {
      toast.error('Registration failed')
    } finally {
      setLoading(false)
    }
  }

  return (
    <AuthShell
      title="Create your account"
      description="Spin up a workspace in seconds. We'll set up tenant isolation, encryption keys, and an audit trail for you."
      footer={
        <>
          Already have one?{' '}
          <Link to="/login" className="font-medium text-foreground underline-offset-4 hover:underline">Sign in</Link>
        </>
      }
    >
      <form onSubmit={handleSubmit} className="space-y-4">
        <Input label="Tenant slug" value={tenantSlug} onChange={(e) => setTenantSlug(e.target.value)} required autoComplete="organization" />
        <Input label="Full name" value={name} onChange={(e) => setName(e.target.value)} required autoFocus autoComplete="name" />
        <Input label="Work email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} required autoComplete="email" />
        <Input label="Password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} required autoComplete="new-password" />
        <p className="text-xs text-muted-foreground">
          By creating an account you agree to the Terms and Privacy Policy.
        </p>
        <Button type="submit" className="w-full" loading={loading}>Create account</Button>
      </form>
    </AuthShell>
  )
}

export const Route = createFileRoute('/register')({ component: RegisterPage })
