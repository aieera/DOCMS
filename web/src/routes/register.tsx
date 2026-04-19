import { createFileRoute, Link } from '@tanstack/react-router'
import { useState } from 'react'
import { Button } from '@/components/ui/Button'
import { Input } from '@/components/ui/Input'
import { register } from '@/api/auth'
import toast from 'react-hot-toast'

function RegisterPage() {
  const [tenantSlug, setTenantSlug] = useState('acme')
  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [loading, setLoading] = useState(false)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setLoading(true)
    try {
      await register(email, password, name, tenantSlug)
      toast.success('Account created — please log in')
    } catch {
      toast.error('Registration failed')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-[var(--color-bg)]">
      <div className="w-full max-w-sm space-y-6 rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-8 shadow-lg">
        <div className="text-center">
          <h1 className="text-2xl font-bold text-[var(--color-primary)]">VaultDMS</h1>
          <p className="mt-1 text-sm text-[var(--color-text-secondary)]">Create your account</p>
        </div>
        <form onSubmit={handleSubmit} className="space-y-4">
          <Input label="Tenant" value={tenantSlug} onChange={(e) => setTenantSlug(e.target.value)} required />
          <Input label="Full Name" value={name} onChange={(e) => setName(e.target.value)} required autoFocus />
          <Input label="Email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} required />
          <Input label="Password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} required />
          <Button type="submit" className="w-full" loading={loading}>Create Account</Button>
        </form>
        <p className="text-center text-sm text-[var(--color-text-secondary)]">Already have an account? <Link to="/login" className="text-[var(--color-primary)] hover:underline">Sign in</Link></p>
      </div>
    </div>
  )
}

export const Route = createFileRoute('/register')({ component: RegisterPage })
