import { copyText } from '@/lib/clipboard'
import { createFileRoute, redirect } from '@tanstack/react-router'
import { useState } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { Trash2, Copy, Check, Plus, ExternalLink, Zap } from 'lucide-react'

import { listAPIKeys, createAPIKey, revokeAPIKey } from '@/api/security'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { Spinner } from '@/components/ui/Spinner'
import { formatDateTime } from '@/lib/formatters'
import {
  Dialog, DialogContent, DialogHeader, DialogTitle,
  DialogDescription, DialogFooter,
} from '@/components/ui/shadcn/dialog'

// /admin/integrations/ipaas — Zapier / Make / n8n connection surface (ADR 0090).
//
// Three concerns on this page:
//   1. API keys with scope (UI for issuing + revoking keys the
//      iPaaS apps will Bearer-auth with).
//   2. Trigger-endpoint reference card showing the polling URLs.
//   3. Getting-started links pointing at the actual Zapier / Make /
//      n8n developer portals where the user builds their apps.

// Standalone URL deep-links into the tabbed Integrations shell; the
// page component below is what the shell renders as its iPaaS tab.
export const Route = createFileRoute('/_authenticated/admin/integrations/ipaas')({
  beforeLoad: () => {
    throw redirect({ to: '/admin/integrations', search: { tab: 'ipaas' }, replace: true })
  },
})

// The scope id IS its label — it's the literal string sent in the token.
// Only the human description comes from the catalogue.
const AVAILABLE_SCOPES = [
  { id: 'integrations:read', descKey: 'scope_read_desc' },
  { id: 'integrations:write', descKey: 'scope_write_desc' },
  { id: 'integrations:*', descKey: 'scope_all_desc' },
]

export function IPaaSPage() {
  const { t } = useTranslation('common')
  const qc = useQueryClient()
  const keysQ = useQuery({ queryKey: ['api-keys'], queryFn: listAPIKeys })
  const [createOpen, setCreateOpen] = useState(false)
  const [revealedKey, setRevealedKey] = useState<{ id: string; plaintext: string } | null>(null)

  const revoke = useAppMutation({
    mutationFn: revokeAPIKey,
    onSuccess: () => {
      toast.success(t('adm.ipaas.revoked'))
      qc.invalidateQueries({ queryKey: ['api-keys'] })
    },
  })

  const origin = typeof window !== 'undefined' ? window.location.origin : 'http://localhost:3000'

  return (
    <div className="space-y-6">
      <PageHeader
        title={t('adm.ipaas.title')}
        description={t('adm.ipaas.description')}
      />

      {/* ---- API keys section ------------------------------------------------ */}
      <section className="mb-8" data-testid="api-keys-section">
        <div className="mb-3 flex items-center justify-between">
          <h2 className="text-lg font-semibold">{t('adm.ipaas.keys')}</h2>
          <Button size="sm" onClick={() => setCreateOpen(true)} data-testid="create-key">
            <Plus className="me-1 h-4 w-4" /> {t('adm.ipaas.new_key')}
          </Button>
        </div>

        {keysQ.isLoading ? <Spinner /> : (keysQ.data ?? []).length === 0 ? (
          <div className="rounded-lg border border-dashed border-border bg-card/40 p-6 text-center text-sm text-muted-foreground">
            {t('adm.ipaas.no_keys')}
          </div>
        ) : (
          <ul className="space-y-2">
            {(keysQ.data ?? []).map((k) => (
              <li
                key={k.key_id}
                className="flex items-center justify-between rounded-lg bg-card p-4 shadow-neu"
                data-testid={`api-key-row-${k.key_id}`}
              >
                <div className="flex-1">
                  <div className="flex items-center gap-2">
                    <span className="font-medium">{k.name}</span>
                    <code dir="ltr" className="rounded bg-muted px-2 py-0.5 font-mono text-xs">{k.key_prefix}…</code>
                  </div>
                  <div className="mt-1 flex flex-wrap gap-1 text-xs">
                    {(k.scopes ?? []).map((s) => (
                      <span key={s} dir="ltr" className="rounded-full bg-muted px-2 py-0.5 font-mono">{s}</span>
                    ))}
                  </div>
                  <p className="mt-1 text-xs text-muted-foreground">
                    {t('adm.ipaas.created_at', { date: formatDateTime(k.created_at) })}
                    {k.last_used_at && t('adm.ipaas.last_used', { date: formatDateTime(k.last_used_at) })}
                    {k.expires_at && t('adm.ipaas.expires', { date: formatDateTime(k.expires_at) })}
                  </p>
                </div>
                <Button
                  size="sm" variant="outline"
                  onClick={() => {
                    if (confirm(t('adm.ipaas.revoke_confirm', { name: k.name }))) {
                      revoke.mutate(k.key_id)
                    }
                  }}
                  data-testid={`revoke-key-${k.key_id}`}
                >
                  <Trash2 className="me-1 h-4 w-4" /> {t('adm.ipaas.revoke')}
                </Button>
              </li>
            ))}
          </ul>
        )}
      </section>

      {/* ---- Trigger endpoints reference ------------------------------------ */}
      <section className="mb-8">
        <h2 className="mb-3 text-lg font-semibold">{t('adm.ipaas.endpoints')}</h2>
        <p className="mb-3 text-sm text-muted-foreground">
          {/* Three literal identifiers mid-sentence, so the markup travels
              with the key; each stays dir="ltr". */}
          <Trans
            i18nKey="adm.ipaas.endpoints_hint"
            ns="common"
            components={Array.from({ length: 3 }, (_, i) => (
              <code key={i} dir="ltr" className="rounded bg-muted px-1 text-xs" />
            ))}
          />
        </p>
        <div className="space-y-2">
          {[
            { trigger: 'document.created', path: '/api/v1/integrations/triggers/documents' },
            { trigger: 'signature.completed', path: '/api/v1/integrations/triggers/signatures/completed' },
            { trigger: 'workflow.completed', path: '/api/v1/integrations/triggers/workflows/completed' },
          ].map((row) => (
            <CopyableEndpoint
              key={row.trigger}
              trigger={row.trigger}
              url={`${origin}${row.path}`}
            />
          ))}
        </div>
      </section>

      {/* ---- Getting started cards ----------------------------------------- */}
      <section>
        <h2 className="mb-3 text-lg font-semibold">{t('adm.ipaas.getting_started')}</h2>
        <p className="mb-3 text-sm text-muted-foreground">
          {t('adm.ipaas.getting_started_hint')}
        </p>
        <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
          <PortalCard
            name="Zapier"
            description={t('adm.ipaas.portal_zapier')}
            url="https://zapier.com/developer/builder"
          />
          <PortalCard
            name="Make"
            description={t('adm.ipaas.portal_make')}
            url="https://www.make.com/en/help/apps/about-the-app-development-platform"
          />
          <PortalCard
            name="n8n"
            description={t('adm.ipaas.portal_n8n')}
            url="https://docs.n8n.io/integrations/creating-nodes/build/declarative-style-node/"
          />
        </div>
      </section>

      <CreateKeyDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        onCreated={(plaintext, id) => {
          setRevealedKey({ id, plaintext })
          qc.invalidateQueries({ queryKey: ['api-keys'] })
        }}
      />

      {revealedKey && (
        <RevealedKeyDialog
          open={!!revealedKey}
          plaintext={revealedKey.plaintext}
          onClose={() => setRevealedKey(null)}
        />
      )}
    </div>
  )
}

// ---- Subcomponents ---------------------------------------------------------

function CopyableEndpoint({ trigger, url }: { trigger: string; url: string }) {
  const { t } = useTranslation('common')
  const [copied, setCopied] = useState(false)
  return (
    <div className="rounded-lg bg-card p-3 shadow-neu">
      <div className="flex items-center justify-between gap-2">
        <div className="flex-1">
          <div className="flex items-center gap-2 text-sm font-medium">
            <Zap className="h-3.5 w-3.5 text-primary" /> <span dir="ltr">{trigger}</span>
          </div>
          <code dir="ltr" className="mt-1 block truncate text-xs text-muted-foreground">{url}</code>
        </div>
        <Button
          size="sm" variant="ghost"
          onClick={() => copyText(url).then(() => { setCopied(true); setTimeout(() => setCopied(false), 1500) })}
          aria-label={t('adm.ipaas.copy_url')}
        >
          {copied ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
        </Button>
      </div>
    </div>
  )
}

function PortalCard({ name, description, url }: { name: string; description: string; url: string }) {
  return (
    <a
      href={url}
      target="_blank"
      rel="noopener noreferrer"
      className="block rounded-lg border border-transparent bg-card p-4 shadow-neu transition-all hover:-translate-y-0.5 hover:border-primary/40"
    >
      <div className="flex items-center gap-2">
        <span className="font-semibold">{name}</span>
        <ExternalLink className="h-3.5 w-3.5 text-muted-foreground" />
      </div>
      <p className="mt-1 text-xs text-muted-foreground">{description}</p>
    </a>
  )
}

function CreateKeyDialog({ open, onOpenChange, onCreated }: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onCreated: (plaintext: string, id: string) => void
}) {
  const { t } = useTranslation('common')
  const [name, setName] = useState('')
  const [scopes, setScopes] = useState<string[]>(['integrations:read'])
  const [expiresInDays, setExpiresInDays] = useState<number | ''>('')

  const create = useAppMutation({
    mutationFn: () => createAPIKey({
      name: name.trim(),
      scopes,
      expires_in_days: expiresInDays === '' ? undefined : Number(expiresInDays),
    }),
    onSuccess: (data) => {
      onCreated(data.api_key, data.key_id)
      onOpenChange(false)
      setName('')
      setScopes(['integrations:read'])
      setExpiresInDays('')
    },
    onError: (e: Error) => toast.error(e.message || t('adm.ipaas.err_create')),
  })

  const toggleScope = (id: string) => {
    setScopes((s) => s.includes(id) ? s.filter((x) => x !== id) : [...s, id])
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{t('adm.ipaas.dlg_title')}</DialogTitle>
          <DialogDescription>
            {t('adm.ipaas.dlg_desc')}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-2">
          <div>
            <label className="mb-1 block text-sm font-medium">{t('adm.ipaas.name')}</label>
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={t('adm.ipaas.name_ph')}
            />
          </div>

          <div>
            <label className="mb-1 block text-sm font-medium">{t('adm.ipaas.scopes')}</label>
            <div className="space-y-1">
              {AVAILABLE_SCOPES.map((s) => (
                <label key={s.id} className="flex cursor-pointer items-start gap-2 text-sm">
                  <input
                    type="checkbox"
                    checked={scopes.includes(s.id)}
                    onChange={() => toggleScope(s.id)}
                    className="mt-0.5"
                  />
                  <div>
                    <code dir="ltr" className="font-mono text-xs">{s.id}</code>
                    <div className="text-xs text-muted-foreground">{t(`adm.ipaas.${s.descKey}`)}</div>
                  </div>
                </label>
              ))}
            </div>
          </div>

          <div>
            <label className="mb-1 block text-sm font-medium">
              {t('adm.ipaas.expires_in')} <span className="text-xs text-muted-foreground">{t('adm.ipaas.expires_in_hint')}</span>
            </label>
            <Input
              type="number"
              value={expiresInDays}
              onChange={(e) => setExpiresInDays(e.target.value === '' ? '' : Number(e.target.value))}
              placeholder={t('adm.ipaas.expires_ph')}
              min={1}
            />
          </div>
        </div>

        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>{t('adm.ipaas.cancel')}</Button>
          <Button
            onClick={() => create.mutate()}
            disabled={name.trim() === '' || scopes.length === 0 || create.isPending}
          >
            {create.isPending ? t('adm.ipaas.creating') : t('adm.ipaas.create')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function RevealedKeyDialog({ open, plaintext, onClose }: {
  open: boolean
  plaintext: string
  onClose: () => void
}) {
  const { t } = useTranslation('common')
  const [copied, setCopied] = useState(false)
  return (
    <Dialog open={open} onOpenChange={(v) => { if (!v) onClose() }}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{t('adm.ipaas.reveal_title')}</DialogTitle>
          <DialogDescription>
            {t('adm.ipaas.reveal_desc')}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-3 py-2">
          <div className="flex items-center gap-2">
            <code dir="ltr" className="flex-1 truncate rounded-lg bg-muted px-3 py-2 font-mono text-xs shadow-neu-inset">
              {plaintext}
            </code>
            <Button
              size="sm"
              onClick={() => copyText(plaintext).then(() => { setCopied(true); setTimeout(() => setCopied(false), 1500) })}
            >
              {copied ? <Check className="me-1 h-4 w-4" /> : <Copy className="me-1 h-4 w-4" />}
              {copied ? t('adm.ipaas.copied') : t('adm.ipaas.copy')}
            </Button>
          </div>
          <p className="text-xs text-muted-foreground">
            {t('adm.ipaas.header_format')}<code dir="ltr" className="rounded bg-muted px-1 font-mono">Bearer {plaintext.slice(0, 20)}…</code>
          </p>
        </div>
        <DialogFooter>
          <Button onClick={onClose}>{t('adm.ipaas.saved')}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
