import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { AlertTriangle, KeyRound, Lock, RotateCw, ShieldOff, CheckCircle2 } from 'lucide-react'
import { toast } from 'sonner'

import { useAppMutation } from '@/hooks/useAppMutation'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { LabeledSelect } from '@/components/ui/shadcn/select'
import { Card } from '@/components/ui/card'
import { Spinner } from '@/components/ui/Spinner'
import {
  getEncryptionStatus,
  registerKMS,
  rotateKEK,
  revokeKEK,
  type KEKVersion,
  type KMSProvider,
} from '@/api/encryption'

// /admin/tenant/encryption — external-KMS / KEK administration (§5/§8).
// Register an external KMS key as the tenant's KEK source, rotate to a new
// version (DEK re-wrap is the operator's `dms-admin kms rewrap` step — blobs
// are never re-encrypted), view rotation history, and revoke a version
// (break-glass). The wrap/unwrap hot path is unchanged; this surface records
// the provider + key reference the envelope layer resolves per tenant.

export const Route = createFileRoute('/_authenticated/admin/tenant/encryption')({
  component: EncryptionPage,
})

const PROVIDER_OPTIONS: { value: KMSProvider; label: string }[] = [
  { value: 'local', label: 'Local (dev — SEDOC_LOCAL_KEK)' },
  { value: 'vault', label: 'HashiCorp Vault (transit)' },
  { value: 'aws_kms', label: 'AWS KMS' },
  { value: 'azure_kv', label: 'Azure Key Vault' },
]

const REF_HINT: Record<KMSProvider, string> = {
  local: 'Not required for the local provider.',
  vault: 'Transit key path, e.g. transit/keys/tenant-<id>',
  aws_kms: 'Key ARN or alias, e.g. arn:aws:kms:eu-west-1:123:key/…',
  azure_kv: 'Key identifier URI, e.g. https://vault.vault.azure.net/keys/kek',
}

function EncryptionPage() {
  const qc = useQueryClient()
  const { data, isLoading, error } = useQuery({
    queryKey: ['admin', 'encryption', 'status'],
    queryFn: getEncryptionStatus,
  })

  const versions = data?.versions ?? []
  const active = versions.find((v) => v.active)

  const invalidate = () => qc.invalidateQueries({ queryKey: ['admin', 'encryption', 'status'] })

  return (
    <div className="mx-auto max-w-4xl p-6">
      <PageHeader
        title="Encryption & key management"
        description="Register an external KMS key as this tenant's KEK source, rotate it, and manage break-glass revocation."
      />

      <CustodyWarning />

      {isLoading && <Spinner />}
      {error && (
        <div className="rounded-lg border border-destructive/40 bg-destructive/5 p-4 text-sm">
          Failed to load key status: {(error as Error).message}
        </div>
      )}

      {data && (
        <>
          <ActiveKeyCard active={active} />
          <RegisterCard active={active} onDone={invalidate} />
          <RotateCard active={active} onDone={invalidate} />
          <HistoryCard versions={versions} onDone={invalidate} />
        </>
      )}
    </div>
  )
}

function CustodyWarning() {
  return (
    <section className="mb-6 rounded-lg border border-warning/40 bg-warning/10 p-4">
      <div className="flex items-start gap-3">
        <AlertTriangle className="mt-0.5 h-5 w-5 shrink-0 text-warning" />
        <div className="text-sm">
          <p className="font-semibold">You hold custody of the external key.</p>
          <p className="mt-1 text-muted-foreground">
            SeDoc wraps each tenant data-encryption key under your KEK; it never stores the KEK
            material itself. If the external key is deleted, disabled, or made unreachable, all data
            encrypted under it becomes permanently unrecoverable — this is the crypto-shredding
            deletion model, and it is irreversible. Rotation mints a new version and re-wraps DEKs;
            it never re-encrypts blobs. Revoking a version is a break-glass action that renders data
            wrapped under it inaccessible.
          </p>
        </div>
      </div>
    </section>
  )
}

function ActiveKeyCard({ active }: { active?: KEKVersion }) {
  return (
    <Card className="mb-6 p-4">
      <div className="mb-3 flex items-center gap-2 text-sm font-semibold">
        <Lock className="h-4 w-4" /> Active key
      </div>
      {active ? (
        <dl className="divide-y divide-border text-sm">
          <Row label="Version" value={`v${active.version}`} />
          <Row label="Provider" value={providerLabel(active.provider)} />
          <Row label="Alias (wrap handle)" value={active.alias} mono />
          <Row label="External key reference" value={active.external_key_ref || '—'} mono />
          <Row label="Created" value={fmt(active.created_at)} />
        </dl>
      ) : (
        <p className="text-sm text-muted-foreground">
          No active KEK version. Provision one with <code>dms-admin kms create</code>, then register
          its provider below.
        </p>
      )}
    </Card>
  )
}

function RegisterCard({ active, onDone }: { active?: KEKVersion; onDone: () => void }) {
  const [provider, setProvider] = useState<KMSProvider>(active?.provider ?? 'local')
  const [ref, setRef] = useState('')

  const register = useAppMutation({
    mutationFn: () => registerKMS({ provider, external_key_ref: ref }),
    onSuccess: () => {
      toast.success('External KMS registered on the active key')
      setRef('')
      onDone()
    },
    onError: (e: unknown) => toast.error(`Register failed: ${(e as Error).message}`),
  })

  const needsRef = provider !== 'local'
  const disabled = register.isPending || !active || (needsRef && ref.trim() === '')

  return (
    <Card className="mb-6 p-4">
      <div className="mb-3 flex items-center gap-2 text-sm font-semibold">
        <KeyRound className="h-4 w-4" /> Register external KMS
      </div>
      <p className="mb-4 text-sm text-muted-foreground">
        Declare which external key backs the active KEK version. The alias stays the wrap handle;
        the envelope layer resolves the provider + reference recorded here.
      </p>
      <div className="grid gap-4 sm:grid-cols-2">
        <LabeledSelect
          label="Provider"
          value={provider}
          onValueChange={(v) => setProvider(v as KMSProvider)}
          options={PROVIDER_OPTIONS}
        />
        <div>
          <label className="mb-1 block text-sm font-medium" htmlFor="kms-ref">
            External key reference
          </label>
          <Input
            id="kms-ref"
            value={ref}
            onChange={(e) => setRef(e.target.value)}
            placeholder={needsRef ? REF_HINT[provider] : 'n/a'}
            disabled={!needsRef}
          />
          <p className="mt-1 text-xs text-muted-foreground">{REF_HINT[provider]}</p>
        </div>
      </div>
      <div className="mt-4">
        <Button onClick={() => register.mutate()} disabled={disabled}>
          {register.isPending ? 'Registering…' : 'Register & make active'}
        </Button>
      </div>
    </Card>
  )
}

function RotateCard({ active, onDone }: { active?: KEKVersion; onDone: () => void }) {
  const [note, setNote] = useState<string | null>(null)

  const rotate = useAppMutation({
    mutationFn: () => rotateKEK({}),
    onSuccess: (res) => {
      toast.success(`Rotated to v${res.live_version}`)
      setNote(res.note)
      onDone()
    },
    onError: (e: unknown) => toast.error(`Rotate failed: ${(e as Error).message}`),
  })

  return (
    <Card className="mb-6 p-4">
      <div className="mb-3 flex items-center gap-2 text-sm font-semibold">
        <RotateCw className="h-4 w-4" /> Rotate key
      </div>
      <p className="mb-4 text-sm text-muted-foreground">
        Retire the live version and mint the next one (carrying the current provider + reference).
        Existing ciphertext still decrypts under the prior version until DEKs are re-wrapped.
      </p>

      {rotate.isPending && (
        <div className="mb-4 flex items-center gap-2 rounded-md bg-muted p-3 text-sm shadow-neu-inset">
          <Spinner />
          <span>Rotating &amp; re-wrapping key metadata…</span>
        </div>
      )}

      {note && !rotate.isPending && (
        <div className="mb-4 rounded-md border border-primary/40 bg-primary/5 p-3 text-sm">
          <p className="font-medium">Next step — re-wrap DEKs</p>
          <p className="mt-1 text-muted-foreground">{note}</p>
        </div>
      )}

      <Button
        variant="outline"
        onClick={() => rotate.mutate()}
        disabled={rotate.isPending || !active}
      >
        {rotate.isPending ? 'Rotating…' : 'Rotate to next version'}
      </Button>
    </Card>
  )
}

function HistoryCard({ versions, onDone }: { versions: KEKVersion[]; onDone: () => void }) {
  const revoke = useAppMutation({
    mutationFn: (version: number) => revokeKEK(version),
    onSuccess: () => {
      toast.success('Version revoked (break-glass)')
      onDone()
    },
    onError: (e: unknown) => toast.error(`Revoke failed: ${(e as Error).message}`),
  })

  const onRevoke = (v: KEKVersion) => {
    if (
      window.confirm(
        `Revoke KEK v${v.version}? Data wrapped only under this version becomes inaccessible. This is a break-glass action.`,
      )
    ) {
      revoke.mutate(v.version)
    }
  }

  return (
    <Card className="p-4">
      <div className="mb-3 text-sm font-semibold">Rotation history</div>
      {versions.length === 0 ? (
        <p className="text-sm text-muted-foreground">No key versions yet.</p>
      ) : (
        <div className="overflow-hidden rounded-lg bg-muted shadow-neu-inset">
          <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="bg-muted/50 text-start text-xs uppercase text-muted-foreground">
              <tr>
                <th className="px-3 py-2">Version</th>
                <th className="px-3 py-2">Provider</th>
                <th className="px-3 py-2">Status</th>
                <th className="px-3 py-2">Created</th>
                <th className="px-3 py-2" />
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {versions.map((v) => (
                <tr key={v.version}>
                  <td className="px-3 py-2 font-mono">v{v.version}</td>
                  <td className="px-3 py-2">{providerLabel(v.provider)}</td>
                  <td className="px-3 py-2">
                    <StatusBadge v={v} />
                  </td>
                  <td className="px-3 py-2 text-muted-foreground">{fmt(v.created_at)}</td>
                  <td className="px-3 py-2 text-end">
                    {!v.revoked_at && (
                      <Button
                        variant="ghost"
                        size="sm"
                        className="gap-1 text-destructive hover:text-destructive/80"
                        onClick={() => onRevoke(v)}
                        disabled={revoke.isPending}
                      >
                        <ShieldOff className="h-3.5 w-3.5" /> Revoke
                      </Button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          </div>
        </div>
      )}
    </Card>
  )
}

function StatusBadge({ v }: { v: KEKVersion }) {
  if (v.revoked_at)
    return (
      <span className="inline-flex items-center gap-1 rounded-full bg-destructive/10 px-2 py-0.5 text-xs text-foreground">
        <ShieldOff className="h-3 w-3" /> Revoked
      </span>
    )
  if (v.active)
    return (
      <span className="inline-flex items-center gap-1 rounded-full bg-success/15 px-2 py-0.5 text-xs text-foreground">
        <CheckCircle2 className="h-3 w-3" /> Active
      </span>
    )
  return (
    <span className="inline-flex items-center gap-1 rounded-full bg-muted px-2 py-0.5 text-xs text-muted-foreground">
      Retired
    </span>
  )
}

function Row({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex items-center justify-between gap-4 py-2">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className={mono ? 'break-all font-mono text-xs' : ''}>{value}</dd>
    </div>
  )
}

function providerLabel(p: KMSProvider): string {
  return PROVIDER_OPTIONS.find((o) => o.value === p)?.label ?? p
}

function fmt(iso?: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString()
}
