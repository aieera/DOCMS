import { useState } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import i18n from '@/i18n'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { AlertTriangle, KeyRound, Lock, RotateCw, ShieldOff, CheckCircle2 } from 'lucide-react'
import { toast } from 'sonner'

import { cn } from '@/lib/cn'
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

// Values only; the labels and hints are catalogue keys resolved at
// render, because a module constant is evaluated once at import.
const PROVIDER_VALUES: KMSProvider[] = ['local', 'vault', 'aws_kms', 'azure_kv']

function EncryptionPage() {
  const { t } = useTranslation('common')
  const qc = useQueryClient()
  const { data, isLoading, error } = useQuery({
    queryKey: ['admin', 'encryption', 'status'],
    queryFn: getEncryptionStatus,
  })

  const versions = data?.versions ?? []
  const active = versions.find((v) => v.active)

  const invalidate = () => qc.invalidateQueries({ queryKey: ['admin', 'encryption', 'status'] })

  return (
    // The old `mx-auto max-w-4xl p-6` pinned 896px of content in the
    // middle of a ~1600px shell and added a third layer of padding on
    // top of app-layout's. Full width now, split into the two things an
    // operator actually does here: settle what backs the key, and work
    // its versions. Each action sits beside the state it changes --
    // Register feeds the Active key card, Rotate appends to the history.
    <div className="space-y-6">
      <PageHeader
        noMargin
        title={t('adm.enc.title')}
        description={t('adm.enc.description')}
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
          {/* The page's status line: what is live right now. */}
          <ActiveKeyCard active={active} />
          <div className="grid gap-6 xl:grid-cols-[minmax(360px,460px)_minmax(0,1fr)]">
            {/* Key source — what the envelope layer resolves per tenant. */}
            <div className="min-w-0">
              <RegisterCard active={active} onDone={invalidate} />
            </div>
            {/* Lifecycle — minting versions and retiring them. */}
            <div className="min-w-0 space-y-6">
              <RotateCard active={active} onDone={invalidate} />
              <HistoryCard versions={versions} onDone={invalidate} />
            </div>
          </div>
        </>
      )}
    </div>
  )
}

function CustodyWarning() {
  const { t } = useTranslation('common')
  return (
    <section className="rounded-lg border border-warning/40 bg-warning/10 p-4">
      <div className="flex items-start gap-3">
        <AlertTriangle className="mt-0.5 h-5 w-5 shrink-0 text-warning" />
        {/* Full-width banner, but the prose is capped to a readable
            measure -- at 1600px an uncapped paragraph runs ~200 chars
            to the line and the eye loses its place on the return. */}
        <div className="max-w-[85ch] text-sm">
          <p className="font-semibold">{t('adm.enc.custody')}</p>
          <p className="mt-1 text-muted-foreground">{t('adm.enc.custody_body')}</p>
        </div>
      </div>
    </section>
  )
}

function ActiveKeyCard({ active }: { active?: KEKVersion }) {
  const { t } = useTranslation('common')
  return (
    <Card className="p-4">
      <div className="mb-3 flex items-center gap-2 text-sm font-semibold">
        <Lock className="h-4 w-4" /> {t('adm.enc.active_key')}
      </div>
      {active ? (
        // Label-above-value, not label/value on one justified line: the
        // key reference is long enough to wrap, and a justified row puts
        // its tail on a second line while the label sits alone on the
        // first. The four short facts band across the width; the
        // reference takes a row of its own.
        <dl className="grid gap-x-8 gap-y-4 text-sm sm:grid-cols-2 lg:grid-cols-4">
          <Fact label={t('adm.enc.version')} value={`v${active.version}`} />
          <Fact label={t('adm.enc.provider')} value={providerLabel(active.provider)} />
          <Fact label={t('adm.enc.alias')} value={active.alias} mono />
          <Fact label={t('adm.enc.created')} value={fmt(active.created_at)} />
          <Fact
            className="sm:col-span-2 lg:col-span-4"
            label={t('adm.enc.external_ref')}
            value={active.external_key_ref || '—'}
            mono
          />
        </dl>
      ) : (
        <p className="text-sm text-muted-foreground">
          <Trans i18nKey="adm.enc.no_active" ns="common" components={{ 1: <code dir="ltr" /> }} />
        </p>
      )}
    </Card>
  )
}

function RegisterCard({ active, onDone }: { active?: KEKVersion; onDone: () => void }) {
  const { t } = useTranslation('common')
  const providerOpts = PROVIDER_VALUES.map((v) => ({ value: v, label: t(`adm.enc.provider_${v}`) }))
  const [provider, setProvider] = useState<KMSProvider>(active?.provider ?? 'local')
  const [ref, setRef] = useState('')

  const register = useAppMutation({
    mutationFn: () => registerKMS({ provider, external_key_ref: ref }),
    onSuccess: () => {
      toast.success(t('adm.enc.registered'))
      setRef('')
      onDone()
    },
    onError: (e: unknown) => toast.error(`Register failed: ${(e as Error).message}`),
  })

  const needsRef = provider !== 'local'
  const disabled = register.isPending || !active || (needsRef && ref.trim() === '')

  return (
    <Card className="p-4">
      <div className="mb-3 flex items-center gap-2 text-sm font-semibold">
        <KeyRound className="h-4 w-4" /> {t('adm.enc.register')}
      </div>
      <p className="mb-4 text-sm text-muted-foreground">
        {t('adm.enc.register_body')}
      </p>
      {/* 2-up while the page is a single wide column; stacked again from
          xl, where this card sits in a ~420px column and a key ARN in a
          190px input would be unreadable. */}
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-1">
        <LabeledSelect
          label={t('adm.enc.provider')}
          value={provider}
          onValueChange={(v) => setProvider(v as KMSProvider)}
          options={providerOpts}
        />
        <div>
          <label className="mb-1 block text-sm font-medium" htmlFor="kms-ref">
            {t('adm.enc.external_ref')}
          </label>
          <Input
            id="kms-ref"
            value={ref}
            onChange={(e) => setRef(e.target.value)}
            placeholder={needsRef ? '' : t('adm.enc.na')}
            disabled={!needsRef}
            aria-describedby="kms-ref-hint"
          />
          <p id="kms-ref-hint" className="mt-1 text-xs text-muted-foreground">
            {t(`adm.enc.hint_${provider}`)}
          </p>
        </div>
      </div>
      <div className="mt-4">
        <Button onClick={() => register.mutate()} disabled={disabled}>
          {register.isPending ? t('adm.enc.registering') : t('adm.enc.register_btn')}
        </Button>
      </div>
    </Card>
  )
}

function RotateCard({ active, onDone }: { active?: KEKVersion; onDone: () => void }) {
  const { t } = useTranslation('common')
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
    <Card className="p-4">
      <div className="mb-3 flex items-center gap-2 text-sm font-semibold">
        <RotateCw className="h-4 w-4" /> {t('adm.enc.rotate')}
      </div>
      <p className="mb-4 text-sm text-muted-foreground">
        {t('adm.enc.rotate_body')}
      </p>

      {rotate.isPending && (
        <div className="mb-4 flex items-center gap-2 rounded-md bg-muted p-3 text-sm">
          <Spinner />
          <span>{t('adm.enc.rotating_msg')}</span>
        </div>
      )}

      {note && !rotate.isPending && (
        <div className="mb-4 rounded-md border border-primary/40 bg-primary/5 p-3 text-sm">
          <p className="font-medium">{t('adm.enc.next_step')}</p>
          <p className="mt-1 text-muted-foreground">{note}</p>
        </div>
      )}

      <Button
        variant="outline"
        onClick={() => rotate.mutate()}
        disabled={rotate.isPending || !active}
      >
        {rotate.isPending ? t('adm.enc.rotating') : t('adm.enc.rotate_btn')}
      </Button>
    </Card>
  )
}

function HistoryCard({ versions, onDone }: { versions: KEKVersion[]; onDone: () => void }) {
  const { t } = useTranslation('common')
  const revoke = useAppMutation({
    mutationFn: (version: number) => revokeKEK(version),
    onSuccess: () => {
      toast.success(t('adm.enc.version_revoked'))
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
      <div className="mb-3 text-sm font-semibold">{t('adm.enc.history')}</div>
      {versions.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t('adm.enc.no_versions')}</p>
      ) : (
        <div className="overflow-hidden rounded-lg bg-muted">
          <div className="overflow-x-auto">
          <table className="w-full text-sm">
            {/* text-start belongs on the th, not the thead: the UA
                stylesheet sets `text-align: center` directly on th, which
                beats a value inherited from thead -- so these headers sat
                centred over start-aligned cells. */}
            <thead className="bg-muted/50 text-xs uppercase text-muted-foreground">
              <tr>
                <th className="px-3 py-2 text-start">{t('adm.enc.version')}</th>
                <th className="px-3 py-2 text-start">{t('adm.enc.provider')}</th>
                <th className="px-3 py-2 text-start">{t('adm.enc.col_status')}</th>
                <th className="px-3 py-2 text-start">{t('adm.enc.created')}</th>
                <th className="px-3 py-2" />
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {versions.map((v) => (
                <tr key={v.version}>
                  <td dir="auto" className="px-3 py-2 font-mono">v{v.version}</td>
                  <td className="px-3 py-2">{providerLabel(v.provider)}</td>
                  <td className="px-3 py-2">
                    <StatusBadge v={v} />
                  </td>
                  <td dir="auto" className="px-3 py-2 text-muted-foreground">{fmt(v.created_at)}</td>
                  <td className="px-3 py-2 text-end">
                    {!v.revoked_at && (
                      <Button
                        variant="ghost"
                        size="sm"
                        className="gap-1 text-destructive hover:text-destructive/80"
                        onClick={() => onRevoke(v)}
                        disabled={revoke.isPending}
                      >
                        <ShieldOff className="h-3.5 w-3.5" /> {t('adm.enc.revoke')}
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
  const { t } = useTranslation('common')
  if (v.revoked_at)
    return (
      <span className="inline-flex items-center gap-1 rounded-full bg-destructive/10 px-2 py-0.5 text-xs text-foreground">
        <ShieldOff className="h-3 w-3" /> {t('adm.enc.status_revoked')}
      </span>
    )
  if (v.active)
    return (
      <span className="inline-flex items-center gap-1 rounded-full bg-success/15 px-2 py-0.5 text-xs text-foreground">
        <CheckCircle2 className="h-3 w-3" /> {t('adm.enc.status_active')}
      </span>
    )
  return (
    <span className="inline-flex items-center gap-1 rounded-full bg-muted px-2 py-0.5 text-xs text-muted-foreground">
      {t('adm.enc.status_retired')}
    </span>
  )
}

function Fact({
  label,
  value,
  mono,
  className,
}: {
  label: string
  value: string
  mono?: boolean
  className?: string
}) {
  return (
    <div className={cn('min-w-0', className)}>
      <dt className="text-xs uppercase tracking-wide text-muted-foreground">{label}</dt>
      <dd dir="auto" className={cn('mt-1 break-all', mono && 'font-mono text-xs')}>{value}</dd>
    </div>
  )
}

function providerLabel(p: KMSProvider): string {
  return i18n.t(`adm.enc.provider_${p}`, { ns: 'common', defaultValue: p })
}

function fmt(iso?: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString()
}
