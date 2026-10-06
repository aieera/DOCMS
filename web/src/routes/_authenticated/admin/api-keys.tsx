import { copyText } from '@/lib/clipboard'
import { Trans, useTranslation } from 'react-i18next'
import { createFileRoute } from '@tanstack/react-router'
import { useState } from 'react'
import { type ColumnDef } from '@tanstack/react-table'
import { Key, Copy, Trash2, AlertTriangle } from 'lucide-react'
import { toast } from 'sonner'

import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { Dialog } from '@/components/ui/Dialog'
import { ConfirmDialog } from '@/components/ui/shadcn/confirm-dialog'
import { EmptyState } from '@/components/ui/EmptyState'
import { Card } from '@/components/ui/card'
import { Badge } from '@/components/ui/shadcn/badge'
import { DataTable } from '@/components/ui/DataTable'
import { useAPIKeys, useCreateAPIKey, useRevokeAPIKey } from '@/hooks/useSecurity'
import type { APIKeyIssued } from '@/api/security'
import { formatRelativeTime } from '@/lib/formatters'

interface APIKeyRow {
  key_id: string
  name: string
  key_prefix: string
  created_at: string
  last_used_at?: string | null
  expires_at?: string | null
  revoked_at?: string | null
}

// Mirrors model.ValidScopes() in services/auth. The ERP integration needs
// documents:read, documents:write, upload, and integrations:read — plus
// documents:delete if it should be able to remove documents it created.
const AVAILABLE_SCOPES = [
  'documents:read',
  'documents:write',
  'documents:delete',
  'upload',
  'search:read',
  'integrations:read',
  'integrations:write',
  'webhooks:manage',
]
const DEFAULT_SCOPES = ['documents:read', 'documents:write']

function ApiKeysPage() {
  const { t } = useTranslation('common')
  const { data: keys, isLoading } = useAPIKeys()
  const createMut = useCreateAPIKey()
  const revokeMut = useRevokeAPIKey()

  const [showCreate, setShowCreate] = useState(false)
  const [name, setName] = useState('')
  const [expiresDays, setExpiresDays] = useState('90')
  const [scopes, setScopes] = useState<string[]>(DEFAULT_SCOPES)
  const [issued, setIssued] = useState<APIKeyIssued | null>(null)
  const [revokeId, setRevokeId] = useState<string | null>(null)

  // QA SD-21: refusals were a native browser bubble (from `required`),
  // a silent numeric min, and toasts — three styles in one dialog. House
  // style is inline-under-the-field after a submit attempt.
  const [submitted, setSubmitted] = useState(false)
  const expiresNum = Number(expiresDays)
  const expiresError = !expiresDays.trim() || Number.isNaN(expiresNum) || expiresNum < 1 || expiresNum > 3650
    ? 'Enter a number of days between 1 and 3650.'
    : undefined
  const handleCreate = async () => {
    setSubmitted(true)
    if (!name.trim() || scopes.length === 0 || expiresError) return
    try {
      const k = await createMut.mutateAsync({
        name: name.trim(),
        scopes,
        expires_in_days: Number(expiresDays) || 90,
      })
      setIssued(k); setName(''); setScopes(DEFAULT_SCOPES)
    } catch {
      toast.error(t('api_keys.create_failed'))
    }
  }

  const handleRevoke = async () => {
    if (!revokeId) return
    try {
      await revokeMut.mutateAsync(revokeId)
      toast.success(t('api_keys.revoked'))
    } catch {
      toast.error(t('api_keys.revoke_failed'))
    } finally {
      setRevokeId(null)
    }
  }

  const columns: ColumnDef<APIKeyRow, unknown>[] = [
    { accessorKey: 'name', header: t('api_keys.col_name'), cell: ({ row }) => <span className="text-sm font-medium">{row.original.name}</span> },
    { accessorKey: 'key_prefix', header: t('api_keys.col_prefix'), cell: ({ row }) => <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs">{row.original.key_prefix}…</code> },
    { accessorKey: 'created_at', header: t('api_keys.col_created'), cell: ({ row }) => <span className="text-xs text-muted-foreground">{formatRelativeTime(row.original.created_at)}</span> },
    { accessorKey: 'last_used_at', header: t('api_keys.col_last_used'), cell: ({ row }) => <span className="text-xs text-muted-foreground">{row.original.last_used_at ? formatRelativeTime(row.original.last_used_at) : '—'}</span> },
    { accessorKey: 'expires_at', header: t('api_keys.col_expires'), cell: ({ row }) => <span className="text-xs text-muted-foreground">{row.original.expires_at ? formatRelativeTime(row.original.expires_at) : t('api_keys.never')}</span> },
    {
      id: 'status',
      header: t('api_keys.col_status'),
      cell: ({ row }) => {
        const k = row.original
        if (k.revoked_at) return <Badge variant="destructive">{t('api_keys.status_revoked')}</Badge>
        if (k.expires_at && new Date(k.expires_at).getTime() < Date.now()) {
          return <Badge variant="secondary">{t('api_keys.status_expired')}</Badge>
        }
        return <Badge variant="default">{t('api_keys.status_active')}</Badge>
      },
    },
    {
      id: 'actions',
      header: '',
      cell: ({ row }) => (
        <div className="flex justify-end">
          {!row.original.revoked_at && (
            <Button variant="ghost" size="sm" onClick={() => setRevokeId(row.original.key_id)} aria-label={t('api_keys.revoke')}>
              <Trash2 className="h-4 w-4 text-destructive" />
            </Button>
          )}
        </div>
      ),
    },
  ]

  return (
    <div className="space-y-6">
      <PageHeader
        title={t('api_keys.title')}
        description={t('api_keys.description')}
        actions={
          <Button onClick={() => { setIssued(null); setShowCreate(true) }} data-testid="new-api-key">
            <Key className="h-4 w-4" /> {t('api_keys.new')}
          </Button>
        }
      />

      {!isLoading && (!keys || keys.length === 0) ? (
        <EmptyState
          icon={<Key className="h-6 w-6" />}
          title={t('api_keys.empty')}
          description={t('api_keys.empty_desc')}
          actionLabel={t('api_keys.empty_action')}
          onAction={() => setShowCreate(true)}
        />
      ) : (
        <DataTable
          columns={columns}
          data={(keys ?? []) as APIKeyRow[]}
          isLoading={isLoading}
          emptyState="No API keys."
        />
      )}

      <Dialog
        open={showCreate}
        onOpenChange={(o) => { setShowCreate(o); if (!o) setIssued(null) }}
        title={issued ? 'API key created' : 'Create API key'}
        description={issued ? 'Copy the secret below — it will not be shown again.' : 'Pick a memorable name and an expiration window.'}
       
      >
        {issued ? (
          <div className="space-y-4">
            <Card className="space-y-2 p-3">
              <div className="flex items-center gap-2">
                <code className="flex-1 truncate rounded bg-muted px-2 py-1.5 font-mono text-xs">{issued.api_key}</code>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => { copyText(issued.api_key); toast.success(t('api_keys.copied')) }}
                  aria-label={t('api_keys.copy')}
                >
                  <Copy className="h-4 w-4" />
                </Button>
              </div>
            </Card>
            {issued.warning && (
              <div className="flex items-start gap-2 rounded-md border border-warning/40 bg-warning/10 p-3 text-xs text-warning-strong">
                <AlertTriangle className="h-4 w-4 shrink-0" />
                <span>{issued.warning}</span>
              </div>
            )}
            <div className="flex justify-end">
              <Button onClick={() => { setShowCreate(false); setIssued(null) }}>{t('api_keys.done')}</Button>
            </div>
          </div>
        ) : (
          <form
            onSubmit={(e) => { e.preventDefault(); handleCreate() }}
            className="space-y-4"
          >
            <Input
              label={t('api_keys.name')}
              placeholder={t('api_keys.name_placeholder')}
              value={name}
              onChange={(e) => setName(e.target.value)}
              error={submitted && !name.trim() ? 'Enter a name for this key.' : undefined}
              autoFocus
            />
            <Input
              label={t('api_keys.expires_days')}
              type="number"
              value={expiresDays}
              onChange={(e) => setExpiresDays(e.target.value)}
              error={submitted ? expiresError : undefined}
            />
            <div className="space-y-1.5">
              <span className="text-sm font-medium">{t('api_keys.scopes')}</span>
              {submitted && scopes.length === 0 && (
                <p className="text-xs font-medium text-destructive">{t('api_keys.scope_required')}</p>
              )}
              <div className="flex flex-wrap gap-2">
                {AVAILABLE_SCOPES.map((s) => {
                  const on = scopes.includes(s)
                  return (
                    <Button
                      key={s}
                      type="button"
                      size="sm"
                      variant={on ? 'default' : 'outline'}
                      onClick={() => setScopes((cur) => on ? cur.filter((x) => x !== s) : [...cur, s])}
                    >
                      {s}
                    </Button>
                  )
                })}
              </div>
              {/* Trans, not t(): four scope names sit inline mid-sentence
                  and Arabic joins a list differently ("و" prefixed, no
                  Oxford comma), so the separators belong to the
                  translation rather than being concatenated around it.
                  The scope identifiers themselves are literals and stay
                  LTR inside the Arabic run. */}
              <p className="text-xs text-muted-foreground">
                <Trans
                  i18nKey="api_keys.erp_hint"
                  ns="common"
                  components={{
                    1: <code className="font-mono" dir="ltr" />,
                    2: <code className="font-mono" dir="ltr" />,
                    3: <code className="font-mono" dir="ltr" />,
                    4: <code className="font-mono" dir="ltr" />,
                  }}
                />
              </p>
            </div>
            <div className="flex justify-end gap-2 pt-2">
              <Button type="button" variant="ghost" onClick={() => setShowCreate(false)}>{t('api_keys.cancel')}</Button>
              <Button type="submit" loading={createMut.isPending}>{t('api_keys.create')}</Button>
            </div>
          </form>
        )}
      </Dialog>

      <ConfirmDialog
        open={!!revokeId}
        onOpenChange={(o) => !o && setRevokeId(null)}
        title={t('api_keys.revoke_title')}
        description={t('api_keys.revoke_desc')}
        confirmLabel="Revoke"
        destructive
        loading={revokeMut.isPending}
        onConfirm={handleRevoke}
      />
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/api-keys')({ component: ApiKeysPage })
