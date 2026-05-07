import { createFileRoute } from '@tanstack/react-router'
import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { Fingerprint, Trash2, ShieldCheck, AlertTriangle, Plus } from 'lucide-react'

import {
  registerPasskey,
  listPasskeys,
  deletePasskey,
  isWebAuthnSupported,
  type PasskeyView,
} from '@/api/webauthn'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { Input } from '@/components/ui/Input'
import { Spinner } from '@/components/ui/Spinner'

// ADR 0070 — /settings/security passkey management.
//
// Lists every cred the user has registered with friendly name +
// transports + "added X ago" + "last used Y ago". "Add passkey"
// prompts for a friendly name first, then runs the WebAuthn
// registration flow. Each row has a Remove action.

function SecuritySettingsPage() {
  const supported = isWebAuthnSupported()
  const qc = useQueryClient()
  const [adding, setAdding] = useState(false)
  const [name, setName] = useState('')

  const { data: passkeys, isLoading } = useQuery({
    queryKey: ['passkeys'],
    queryFn: listPasskeys,
  })

  const addMut = useMutation({
    mutationFn: () => registerPasskey(name),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['passkeys'] })
      toast.success('Passkey added')
      setAdding(false)
      setName('')
    },
    onError: (err: { message?: string; response?: { status?: number; data?: { error?: string } } }) => {
      const detail = err.response?.data?.error ?? err.message ?? 'Could not add passkey'
      // 501 surfaces when VAULTDMS_WEBAUTHN_RPID isn't set — distinct
      // from a user-cancelled creation.
      if (err.response?.status === 501) {
        toast.error('Passkeys not configured for this deploy. Contact your admin.')
      } else {
        toast.error(detail)
      }
    },
  })

  const removeMut = useMutation({
    mutationFn: (credentialID: string) => deletePasskey(credentialID),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['passkeys'] })
      toast.success('Passkey removed')
    },
    onError: () => toast.error('Could not remove'),
  })

  const handleAdd = () => {
    if (!name.trim()) {
      toast.error('Give this passkey a name (e.g. "Work laptop")')
      return
    }
    addMut.mutate()
  }

  return (
    <div className="mx-auto max-w-3xl">
      <PageHeader
        title="Security"
        description="Manage passkeys and other authentication factors. Passkeys are phishing-resistant and let you sign in without a password."
      />

      {!supported && (
        <div className="mb-4 rounded-md border border-amber-300 bg-amber-50 p-3 text-sm text-amber-900 dark:border-amber-700 dark:bg-amber-950 dark:text-amber-200">
          <div className="flex items-start gap-2">
            <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
            <div>
              Your browser doesn&apos;t support passkeys. Use a recent Chrome, Edge, Safari, or Firefox to register one.
            </div>
          </div>
        </div>
      )}

      <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4">
        <div className="mb-4 flex items-center justify-between">
          <h3 className="flex items-center gap-2 text-sm font-semibold">
            <Fingerprint className="h-4 w-4" />
            Passkeys
          </h3>
          <Button
            onClick={() => setAdding(true)}
            disabled={!supported}
            data-testid="add-passkey"
          >
            <Plus className="h-4 w-4" /> Add passkey
          </Button>
        </div>

        {isLoading ? (
          <div className="flex justify-center py-6"><Spinner className="h-5 w-5" /></div>
        ) : (passkeys ?? []).length === 0 ? (
          <div className="rounded-md border border-dashed border-[var(--color-border)] p-6 text-center text-sm text-[var(--color-text-secondary)]">
            <ShieldCheck className="mx-auto mb-2 h-8 w-8 opacity-50" />
            <p>No passkeys yet. Add one for phishing-resistant sign-in.</p>
          </div>
        ) : (
          <ul className="space-y-2" data-testid="passkey-list">
            {(passkeys ?? []).map((p) => <PasskeyRow key={p.credential_id} p={p} onRemove={(id) => removeMut.mutate(id)} removing={removeMut.isPending} />)}
          </ul>
        )}
      </div>

      <Dialog
        open={adding}
        onOpenChange={(o) => { if (!o) { setAdding(false); setName('') } }}
        title="Add a passkey"
        size="md"
      >
        <div className="space-y-3" data-testid="add-passkey-dialog">
          <p className="text-sm text-[var(--color-text-secondary)]">
            Give this passkey a name so you recognize it in the list. The name is local to your account; the authenticator (Yubikey, Touch ID, etc.) doesn&apos;t see it.
          </p>
          <Input
            label="Friendly name"
            placeholder="e.g. Work laptop, Phone, Yubikey at desk"
            value={name}
            onChange={(e) => setName(e.target.value)}
            autoFocus
            data-testid="passkey-name"
          />
          <div className="flex justify-end gap-2 pt-2">
            <Button variant="ghost" onClick={() => { setAdding(false); setName('') }}>Cancel</Button>
            <Button
              onClick={handleAdd}
              disabled={addMut.isPending || !name.trim()}
              data-testid="passkey-confirm"
            >
              {addMut.isPending ? <Spinner className="h-4 w-4" /> : <Fingerprint className="h-4 w-4" />}
              Continue
            </Button>
          </div>
        </div>
      </Dialog>
    </div>
  )
}

function PasskeyRow({ p, onRemove, removing }: {
  p: PasskeyView
  onRemove: (id: string) => void
  removing: boolean
}) {
  return (
    <li
      className="flex items-center justify-between rounded-md border border-[var(--color-border)] p-3"
      data-testid={`passkey-row-${p.credential_id}`}
    >
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <Fingerprint className="h-4 w-4 text-[var(--color-primary)]" />
          <span className="font-medium">{p.name}</span>
          {p.backup_state && (
            <span className="rounded-full bg-emerald-100 px-2 py-0.5 text-xs text-emerald-800 dark:bg-emerald-900 dark:text-emerald-200">
              Synced
            </span>
          )}
        </div>
        <p className="mt-1 text-xs text-[var(--color-text-secondary)]">
          {p.transports.length > 0 ? p.transports.join(' · ') : 'unknown transport'}
          {' · added '}{relativeTime(p.created_at)}
          {p.last_used_at ? ` · last used ${relativeTime(p.last_used_at)}` : ' · never used'}
        </p>
      </div>
      <Button
        variant="ghost"
        onClick={() => {
          if (confirm(`Remove passkey "${p.name}"?`)) onRemove(p.credential_id)
        }}
        disabled={removing}
        data-testid={`remove-passkey-${p.credential_id}`}
      >
        <Trash2 className="h-4 w-4 text-red-500" />
      </Button>
    </li>
  )
}

function relativeTime(iso: string): string {
  const ms = Date.now() - new Date(iso).getTime()
  if (ms < 60_000) return 'just now'
  if (ms < 3_600_000) return `${Math.floor(ms / 60_000)} min ago`
  if (ms < 86_400_000) return `${Math.floor(ms / 3_600_000)} hr ago`
  return `${Math.floor(ms / 86_400_000)} d ago`
}

export const Route = createFileRoute('/_authenticated/settings/security')({
  component: SecuritySettingsPage,
})
