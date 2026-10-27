import { PasswordInput } from '@/components/ui/PasswordInput'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'

import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import {
  Form, FormField, FormItem, FormLabel, FormControl, FormMessage,
} from '@/components/ui/form'
import { AuthShell } from '@/components/layout/auth-shell'
import { register } from '@/api/auth'
import { readErrorMessage } from '@/api/client'

// L-5 — auth forms standardized on useForm + zodResolver via the shadcn
// form.tsx primitive (replaces the raw useState + manual validation that
// every auth screen used to carry). The Zod schema is the single source
// of client-side validation; the submit handler keeps the existing
// backend call + the H-6 interceptor-aware error branches verbatim.
const registerSchema = z.object({
  tenantSlug: z.string().trim().min(1, 'Tenant slug is required'),
  name: z.string().trim().min(1, 'Full name is required'),
  email: z.string().trim().min(1, 'Work email is required').email('Enter a valid email address'),
  password: z.string().min(1, 'Password is required'),
})

type RegisterValues = z.infer<typeof registerSchema>

function RegisterPage() {
  const navigate = useNavigate()
  const form = useForm<RegisterValues>({
    resolver: zodResolver(registerSchema),
    defaultValues: { tenantSlug: 'acme', name: '', email: '', password: '' },
  })

  const onSubmit = async (values: RegisterValues) => {
    try {
      await register(values.email, values.password, values.name, values.tenantSlug)
      toast.success('Account created — please log in')
      setTimeout(() => navigate({ to: '/login' }), 800)
    } catch (err: unknown) {
      // H-6: only surface what the response interceptor in api/client.ts
      // doesn't already toast. Today the interceptor handles:
      //   - 401 (logs out + redirects; no toast)
      //   - 403 (toasts "Access denied — {detail}")
      //   - 429 (toasts the rate-limit message)
      //   - 5xx (toasts "Server error: {detail}")
      //   - 400 with a parsable error body (toasts the field message)
      // Gaps we cover here: 409 (email conflict), 400 with no detail,
      // and the no-response / network case. Everything else falls
      // through silently — the interceptor already showed a toast and
      // a second one would just confuse users.
      const status = (err as { response?: { status?: number } } | null)?.response?.status
      const detail = readErrorMessage(err)
      if (status === 409) {
        toast.error(detail ?? 'That email is already registered. Try signing in instead.')
      } else if (status === 400 && !detail) {
        // Interceptor stays silent on 400 with no parsable body.
        toast.error('Check the form for invalid fields and try again.')
      } else if (status === undefined) {
        // No HTTP response at all — network down, CORS rejection,
        // proxy unreachable. Interceptor doesn't handle this either.
        toast.error("Couldn't reach the auth service. Check your connection and try again.")
      }
      // 401 / 403 / 429 / 5xx / 400-with-detail: interceptor already
      // toasted. Stay quiet here so the user sees one message, not two.
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
      <Form {...form}>
        <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
          <FormField
            control={form.control}
            name="tenantSlug"
            render={({ field }) => (
              <FormItem>
                <FormLabel>Tenant slug</FormLabel>
                <FormControl>
                  <Input {...field} autoComplete="organization" />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name="name"
            render={({ field }) => (
              <FormItem>
                <FormLabel>Full name</FormLabel>
                <FormControl>
                  <Input {...field} autoFocus autoComplete="name" />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name="email"
            render={({ field }) => (
              <FormItem>
                <FormLabel>Work email</FormLabel>
                <FormControl>
                  <Input {...field} type="email" autoComplete="email" />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name="password"
            render={({ field }) => (
              <FormItem>
                <FormLabel>Password</FormLabel>
                <FormControl>
                  <PasswordInput {...field} autoComplete="new-password" />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
          <p className="text-xs text-muted-foreground">
            By creating an account you agree to the Terms and Privacy Policy.
          </p>
          <Button type="submit" className="w-full" loading={form.formState.isSubmitting}>Create account</Button>
        </form>
      </Form>
    </AuthShell>
  )
}

export const Route = createFileRoute('/register')({ component: RegisterPage })
