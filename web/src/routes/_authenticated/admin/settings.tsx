import { createFileRoute } from '@tanstack/react-router'
import { useState } from 'react'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { Dialog } from '@/components/ui/Dialog'
import { Skeleton } from '@/components/ui/Skeleton'
import { ShieldCheck, Smartphone, Trash2, Copy } from 'lucide-react'
import { toast } from 'sonner'
import {
  useSetupMFA, useConfirmMFA, useDisableMFA,
  useSessions, useRevokeSession, useRevokeAllOtherSessions,
} from '@/hooks/useSecurity'
import { useCurrentUser } from '@/hooks/useAuth'
import type { MFASetupResult } from '@/api/security'
import { formatRelativeTime } from '@/lib/formatters'

function SettingsPage() {
  return (
    <div className="space-y-8">
      <PageHeader title="Security settings" description="Multi-factor authentication and active sessions" />
      <MFACard />
      <SessionsCard />
    </div>
  )
}

function MFACard() {
  const user = useCurrentUser()
  const setupMut = useSetupMFA()
  const confirmMut = useConfirmMFA()
  const disableMut = useDisableMFA()

  const [setupData, setSetupData] = useState<MFASetupResult | null>(null)
  const [totp, setTotp] = useState('')
  const [showDisable, setShowDisable] = useState(false)
  const [disableCode, setDisableCode] = useState('')

  const enabled = !!user?.mfa_enabled

  const startSetup = async () => {
    try {
      const data = await setupMut.mutateAsync()
      setSetupData(data)
    } catch {
      toast.error('Failed to start MFA enrollment')
    }
  }

  const confirmSetup = async () => {
    try {
      await confirmMut.mutateAsync(totp)
      toast.success('MFA enabled')
      setSetupData(null)
      setTotp('')
    } catch {
      toast.error('Invalid code')
    }
  }

  const disable = async () => {
    try {
      await disableMut.mutateAsync({ totp_code: disableCode })
      toast.success('MFA disabled')
      setShowDisable(false)
      setDisableCode('')
    } catch {
      toast.error('Invalid code')
    }
  }

  return (
    <section className="rounded-lg border border-border p-6">
      <div className="flex items-start gap-4">
        <ShieldCheck className="mt-0.5 h-6 w-6 text-primary" />
        <div className="flex-1">
          <h2 className="text-lg font-semibold">Multi-factor authentication</h2>
          <p className="mt-1 text-sm text-muted-foreground">
            {enabled
              ? 'MFA is enabled. You are prompted for a TOTP code on each login.'
              : 'Add a second factor (TOTP authenticator app) to your login.'}
          </p>
          <div className="mt-4">
            {enabled ? (
              <Button variant="ghost" onClick={() => setShowDisable(true)}>Disable MFA</Button>
            ) : (
              <Button onClick={startSetup} disabled={setupMut.isPending}>
                {setupMut.isPending ? 'Starting…' : 'Enable MFA'}
              </Button>
            )}
          </div>
        </div>
      </div>

      <Dialog
        open={!!setupData}
        onOpenChange={(o) => { if (!o) { setSetupData(null); setTotp('') } }}
        title="Enable MFA"
       
      >
        {setupData && (
          <div className="space-y-3">
            <p className="text-sm">Scan this URI with your authenticator app (Google Authenticator, 1Password, etc.):</p>
            <div className="flex items-center gap-2 rounded-md border border-border bg-muted/40 p-2 ">
              <code className="flex-1 truncate font-mono text-xs">{setupData.qr_code_uri}</code>
              <Button
                variant="ghost"
                size="sm"
                onClick={() => { navigator.clipboard.writeText(setupData.qr_code_uri); toast.success('Copied') }}
              >
                <Copy className="h-4 w-4" />
              </Button>
            </div>
            <p className="text-xs text-muted-foreground">Manual secret: <code className="font-mono">{setupData.secret}</code></p>
            <div className="rounded-md border border-warning/40 bg-warning/10 p-3 text-xs ">
              <p className="mb-1 font-medium">Save these recovery codes — shown once:</p>
              <div className="grid grid-cols-2 gap-1 font-mono">
                {setupData.recovery_codes.map((c) => <code key={c}>{c}</code>)}
              </div>
            </div>
            <Input label="Enter TOTP code to confirm" value={totp} onChange={(e) => setTotp(e.target.value)} />
            <div className="flex justify-end gap-2">
              <Button variant="ghost" onClick={() => { setSetupData(null); setTotp('') }}>Cancel</Button>
              <Button onClick={confirmSetup} disabled={confirmMut.isPending || totp.length < 6}>
                {confirmMut.isPending ? 'Verifying…' : 'Confirm & enable'}
              </Button>
            </div>
          </div>
        )}
      </Dialog>

      <Dialog
        open={showDisable}
        onOpenChange={(o) => { setShowDisable(o); if (!o) setDisableCode('') }}
        title="Disable MFA"
        size="sm"
      >
        <div className="space-y-3">
          <p className="text-sm">Enter a current TOTP code to disable MFA.</p>
          <Input label="TOTP code" value={disableCode} onChange={(e) => setDisableCode(e.target.value)} />
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={() => setShowDisable(false)}>Cancel</Button>
            <Button onClick={disable} disabled={disableMut.isPending}>
              {disableMut.isPending ? 'Disabling…' : 'Disable'}
            </Button>
          </div>
        </div>
      </Dialog>
    </section>
  )
}

function SessionsCard() {
  const { data: sessions, isLoading } = useSessions()
  const revokeMut = useRevokeSession()
  const revokeAllMut = useRevokeAllOtherSessions()

  const handleRevokeAll = async () => {
    if (!confirm('Sign out every other device?')) return
    try {
      const r = await revokeAllMut.mutateAsync()
      toast.success(`Revoked ${r.revoked} session(s)`)
    } catch {
      toast.error('Failed to revoke sessions')
    }
  }

  return (
    <section className="rounded-lg border border-border p-6">
      <div className="flex items-start gap-4">
        <Smartphone className="mt-0.5 h-6 w-6 text-primary" />
        <div className="flex-1">
          <div className="flex items-center justify-between">
            <div>
              <h2 className="text-lg font-semibold">Active sessions</h2>
              <p className="mt-1 text-sm text-muted-foreground">Devices where you are signed in.</p>
            </div>
            <Button variant="ghost" onClick={handleRevokeAll} disabled={revokeAllMut.isPending}>
              Sign out other devices
            </Button>
          </div>
          <div className="mt-4">
            {isLoading ? (
              <Skeleton className="h-24" />
            ) : !sessions || sessions.length === 0 ? (
              <p className="text-sm text-muted-foreground">No active sessions.</p>
            ) : (
              <ul className="divide-y divide-border">
                {sessions.map((s) => (
                  <li key={s.id} className="flex items-center justify-between py-3">
                    <div>
                      <p className="text-sm font-medium">
                        {s.user_agent || 'Unknown device'} {s.is_current && <span className="ml-2 rounded bg-green-100 px-1.5 py-0.5 text-xs text-green-800 dark:bg-green-900/50 dark:text-green-200">This device</span>}
                      </p>
                      <p className="mt-0.5 text-xs text-muted-foreground">
                        {s.ip_address} · last active {formatRelativeTime(s.last_activity_at)}
                      </p>
                    </div>
                    {!s.is_current && (
                      <Button variant="ghost" size="sm" onClick={() => revokeMut.mutate(s.id)} aria-label="Revoke session">
                        <Trash2 className="h-4 w-4" />
                      </Button>
                    )}
                  </li>
                ))}
              </ul>
            )}
          </div>
        </div>
      </div>
    </section>
  )
}

export const Route = createFileRoute('/_authenticated/admin/settings')({ component: SettingsPage })
