import { createFileRoute, Link } from '@tanstack/react-router'
import { KeyRound } from 'lucide-react'

import { AuthShell } from '@/components/layout/auth-shell'
import { ComingSoon } from '@/components/shared/ComingSoon'

// M-6: the previous handler was a setTimeout that flashed a toast and
// pretended an email had been sent — no backend route exists for
// password reset. Auth service router enumeration (recon in the
// matching commit message) shows /forgot-password and /reset-password
// have never been built. Rather than fake a feature, this route now
// renders a ComingSoon panel that ALSO tells the locked-out user
// what to do right now: contact their tenant admin, who can re-issue
// access via /admin/users/invite (auth admin.go:119) which sets a
// password directly during invite. That's an existing, working path.
function ForgotPasswordPage() {
  return (
    <AuthShell
      title="Reset your password"
      description="A self-service password-reset flow is on the roadmap. Until then, your administrator can re-issue access for you."
      footer={
        <Link to="/login" className="font-medium text-foreground underline-offset-4 hover:underline">
          Back to sign in
        </Link>
      }
    >
      <ComingSoon
        icon={<KeyRound className="h-6 w-6" />}
        title="Self-service password reset isn't ready yet"
        description="In the meantime, contact your tenant administrator — they can re-issue your access through the admin invite flow."
        eyebrow="Backend pending"
        bullets={[
          'Admins: /admin/users → Invite (set password during invite)',
          'Email a reset link with single-use token — backend pending',
          'New password must meet the same policy as registration',
        ]}
      />
    </AuthShell>
  )
}

export const Route = createFileRoute('/forgot-password')({ component: ForgotPasswordPage })
