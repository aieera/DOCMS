// ADR 0063 — recovery codes display + regenerate. The codes
// themselves are issued by /auth/mfa/setup (existing TOTP setup
// flow) and on regenerate. Server returns plaintext codes EXACTLY
// ONCE; we render them and prompt the user to copy them.
import { useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { Copy, RotateCcw, AlertTriangle } from 'lucide-react'
import { api, readErrorMessage } from '@/api/client'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Spinner } from '@/components/ui/Spinner'
import { DirectionalIcon } from '@/components/shared/DirectionalIcon'

export const Route = createFileRoute('/_authenticated/settings/security/mfa/recovery')({
  component: RecoveryCodesPage,
})

interface RegenerateResponse {
  recovery_codes: string[]
}

async function regenerateRecoveryCodes(): Promise<string[]> {
  // Reuses the existing /auth/mfa/setup flow for now — calling
  // setup again rotates the codes when MFA is already enabled.
  const { data } = await api.post<RegenerateResponse>('/auth/mfa/setup')
  return data.recovery_codes ?? []
}

function RecoveryCodesPage() {
  const [codes, setCodes] = useState<string[] | null>(null)
  const regen = useAppMutation({
    mutationFn: regenerateRecoveryCodes,
    onSuccess: (cs) => {
      setCodes(cs)
      toast.success('New recovery codes generated. Old codes are now invalid.')
    },
    onError: (e: unknown) => toast.error(readErrorMessage(e) ?? 'Could not regenerate recovery codes'),
  })

  const copyAll = () => {
    if (!codes) return
    navigator.clipboard.writeText(codes.join('\n'))
    toast.success('Copied to clipboard')
  }

  return (
    <div className="mx-auto max-w-2xl space-y-4">
      <Link to="/settings/security/mfa" className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:underline">
        <DirectionalIcon name="ArrowLeft" className="h-3 w-3" /> Back to MFA settings
      </Link>
      <PageHeader
        title="Recovery codes"
        description="Single-use codes that let you sign in if you lose every other factor. Store them in a password manager."
      />

      <div className="flex items-start gap-2 rounded border border-warning/40 bg-warning/10 p-3 text-sm text-warning">
        <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
        <div>
          Generating new codes <strong>invalidates every previous code</strong>.
          Each code can be used exactly once.
        </div>
      </div>

      <div className="rounded-lg border border-border bg-card p-6">
        {!codes && !regen.isPending && (
          <Button onClick={() => regen.mutate()}>
            <RotateCcw className="h-4 w-4" /> Generate new recovery codes
          </Button>
        )}

        {regen.isPending && (
          <div className="flex items-center gap-2 text-sm">
            <Spinner /> Generating…
          </div>
        )}

        {codes && (
          <>
            <p className="mb-3 text-sm">
              Save these now — they will not be shown again.
            </p>
            <ul
              data-testid="recovery-codes-list"
              className="grid grid-cols-2 gap-2 rounded border border-border bg-muted p-3 font-mono text-sm"
            >
              {codes.map((c) => (
                <li key={c} className="select-all">{c}</li>
              ))}
            </ul>
            <div className="mt-4 flex gap-2">
              <Button variant="default" onClick={copyAll}>
                <Copy className="h-4 w-4" /> Copy all
              </Button>
              <Button variant="ghost" onClick={() => regen.mutate()}>
                <RotateCcw className="h-4 w-4" /> Regenerate
              </Button>
            </div>
          </>
        )}
      </div>
    </div>
  )
}
