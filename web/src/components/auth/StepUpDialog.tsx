// ADR 0061 — Step-up modal.
//
// Triggered when an axios response carries
// `X-Step-Up-Required: webauthn`. Prompts the user to re-prove
// presence with a passkey; on success, refreshes the
// step_up_grants window and re-issues the failed request via the
// caller-supplied `onComplete` callback.
//
// The component does NOT auto-mount — caller controls visibility.
// A typical pattern is a single instance high in the tree with a
// store-driven open prop; an axios interceptor sets the flag when
// it sees a 401 + the header.

import { useState } from 'react'
import { Fingerprint, AlertTriangle } from 'lucide-react'
import { toast } from 'sonner'

import { Dialog } from '@/components/ui/Dialog'
import { Button } from '@/components/ui/shadcn/button'
import { Spinner } from '@/components/ui/Spinner'
import { stepUp, isWebAuthnSupported } from '@/api/webauthn'
import { useAuthStore } from '@/store/authStore'

interface Props {
  open: boolean
  // Reason copy shown above the prompt — the calling page passes
  // something like "Releasing a legal hold is irreversible". Empty
  // → generic copy.
  reason?: string
  // Called after a successful step-up. Caller typically retries the
  // sensitive operation that triggered the prompt.
  onComplete: () => void
  onCancel: () => void
}

export function StepUpDialog({ open, reason, onComplete, onCancel }: Props) {
  const user = useAuthStore((s) => s.user)
  const [busy, setBusy] = useState(false)

  const supported = isWebAuthnSupported()

  const handleVerify = async () => {
    if (!user?.email) {
      toast.error('Not logged in')
      return
    }
    setBusy(true)
    try {
      // The tenant slug isn't on the user object today; use the
      // canonical "default" until the auth store carries it. The
      // backend resolves the tenant via the email lookup anyway.
      await stepUp({ tenant_slug: 'default', email: user.email })
      toast.success('Verified — proceeding')
      onComplete()
    } catch (e) {
      toast.error('Verification failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => !o && onCancel()}
      title="Verify your identity"
     
    >
      <div className="space-y-4" data-testid="stepup-dialog">
        <div className="flex items-start gap-3 rounded-md border border-amber-300 bg-amber-50 p-3 text-sm dark:border-amber-700 dark:bg-amber-950">
          <AlertTriangle className="mt-0.5 h-5 w-5 shrink-0 text-amber-600" />
          <div className="text-amber-900 dark:text-amber-200">
            <p className="font-medium">Sensitive action — fresh passkey check required.</p>
            <p className="mt-1 text-xs">
              {reason ?? 'This operation is recorded and irreversible. Please verify with your passkey to continue.'}
            </p>
            <p className="mt-1 text-xs">
              Verification stays valid for 5 minutes — you won&apos;t be re-prompted for follow-up actions in that window.
            </p>
          </div>
        </div>

        {!supported && (
          <div className="rounded-md border border-red-300 bg-red-50 p-3 text-sm text-red-900 dark:border-red-700 dark:bg-red-950 dark:text-red-200">
            Your browser doesn&apos;t support passkeys. Use a recent Chrome, Edge, Safari, or Firefox to continue this operation.
          </div>
        )}

        <div className="flex justify-end gap-2">
          <Button variant="ghost" onClick={onCancel} disabled={busy}>Cancel</Button>
          <Button
            onClick={handleVerify}
            disabled={busy || !supported}
            data-testid="stepup-verify"
          >
            {busy ? <Spinner className="h-4 w-4" /> : <Fingerprint className="h-4 w-4" />}
            Verify with passkey
          </Button>
        </div>
      </div>
    </Dialog>
  )
}
