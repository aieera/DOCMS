import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { createFileRoute, redirect } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { Plug, ExternalLink, CheckCircle2, AlertCircle, Sparkles } from 'lucide-react'

import { api } from '@/api/client'
import { PageHeader } from '@/components/shared/PageHeader'
import { Card } from '@/components/ui/card'
import { Button } from '@/components/ui/shadcn/button'
import { Badge } from '@/components/ui/shadcn/badge'
import { Skeleton } from '@/components/ui/Skeleton'
import { M365ConnectorModal } from '@/components/admin/M365ConnectorModal'

// /admin/connectors — list installable providers and surface which
// ones are already authorized for the tenant. The page previously
// rendered a permanent "No connectors installed" empty state with
// no way to install anything (BUG-17).

interface ProviderDef {
  id: string
  /** Vendor name — a proper noun, so it is NOT translated. The human
   *  description comes from the catalogue as adm.conn.d_<id>. */
  label: string
  /** comingSoon = no backend provider exists yet under
   *  services/connector/internal/providers. The card shows a
   *  "Coming soon" badge instead of "Available", and the Install
   *  button is disabled so admins don't kick off an OAuth flow
   *  that would dead-end at the stub /auth-url handler. */
  comingSoon?: boolean
}

const CATALOG: ProviderDef[] = [
  { id: 'salesforce',   label: 'Salesforce' },
  { id: 'google_drive', label: 'Google Drive' },
  { id: 'm365',         label: 'Microsoft 365' },
  { id: 'dropbox',      label: 'Dropbox', comingSoon: true },
  { id: 'box',          label: 'Box', comingSoon: true },
]

interface InstalledConnector {
  provider: string
  connected_at?: string
  status?: string
}

export function ConnectorsPage() {
  const { t } = useTranslation('common')
  const qc = useQueryClient()
  // ADR 0111 — M365 has a real flow (save credentials → modal → OAuth
  // round-trip); the other tiles still hit the stub `/auth-url`
  // endpoint until their providers get the same treatment.
  const [m365Open, setM365Open] = useState(false)

  const { data, isLoading } = useQuery({
    queryKey: ['admin', 'connectors'],
    queryFn: async () => {
      const r = await api.get<InstalledConnector[]>('/connectors')
      return r.data ?? []
    },
  })

  // Redirect URI mirrors the backend default at
  // services/connector/cmd/server/main.go:75. Same value for every
  // provider — the connector framework dispatches by the HMAC-signed
  // state, not the redirect URI.
  const redirectURI =
    typeof window !== 'undefined'
      ? `${window.location.origin}/api/v1/connectors/oauth/callback`
      : '/api/v1/connectors/oauth/callback'

  const install = useAppMutation({
    mutationFn: async (provider: string) => {
      // Backend exposes a stub /auth-url endpoint today. Once the
      // real OAuth handshake lands, redirect_url comes back here.
      const r = await api.get<{ redirect_url?: string; status?: string }>(`/connectors/${provider}/auth-url`)
      return { provider, ...r.data }
    },
    onSuccess: (out) => {
      if (out.redirect_url) {
        window.location.href = out.redirect_url
        return
      }
      // Stub backend — surface the gap clearly instead of silently
      // pretending to install.
      toast.message(t('adm.conn.stub_title', { provider: out.provider }), {
        description: t('adm.conn.stub_desc'),
      })
      qc.invalidateQueries({ queryKey: ['admin', 'connectors'] })
    },
    onError: (e: Error) => toast.error(e.message || t('adm.conn.err_install')),
  })

  const handleInstallClick = (id: string) => {
    if (id === 'm365') {
      setM365Open(true)
      return
    }
    install.mutate(id)
  }

  const installed = new Map<string, InstalledConnector>(
    (data ?? []).map((c) => [c.provider, c] as const),
  )

  return (
    <div className="space-y-6">
      <PageHeader
        title={t('adm.conn.title')}
        description={t('adm.conn.description')}
      />

      {isLoading ? (
        <Skeleton className="h-48" />
      ) : (
        <ul className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3" data-testid="connector-catalog">
          {CATALOG.map((p) => {
            const isInstalled = installed.has(p.id)
            return (
              <li key={p.id}>
                <Card className="flex h-full flex-col p-4">
                  <header className="mb-2 flex items-center justify-between gap-2">
                    <h3 className="flex items-center gap-2 text-sm font-semibold">
                      <Plug className="h-4 w-4 text-muted-foreground" />
                      {p.label}
                    </h3>
                    {isInstalled ? (
                      <Badge variant="active"><CheckCircle2 className="me-1 h-3 w-3" /> {t('adm.conn.installed')}</Badge>
                    ) : p.comingSoon ? (
                      <Badge variant="outline">{t('adm.conn.coming_soon')}</Badge>
                    ) : (
                      <Badge variant="draft">{t('adm.conn.available')}</Badge>
                    )}
                  </header>
                  <p className="flex-1 text-xs text-muted-foreground">{t(`adm.conn.d_${p.id}`)}</p>
                  <div className="mt-3">
                    <Button
                      size="sm"
                      variant={isInstalled ? 'outline' : 'default'}
                      onClick={() => handleInstallClick(p.id)}
                      loading={install.isPending && install.variables === p.id}
                      disabled={p.comingSoon}
                      title={p.comingSoon ? t('adm.conn.not_ready') : undefined}
                      data-testid={`install-${p.id}`}
                    >
                      <Sparkles className="me-1 h-3.5 w-3.5" />
                      {p.comingSoon ? t('adm.conn.not_available') : isInstalled ? t('adm.conn.reauthorize') : t('adm.conn.install')}
                      {!p.comingSoon && <ExternalLink className="ms-1 h-3 w-3" />}
                    </Button>
                  </div>
                </Card>
              </li>
            )
          })}
        </ul>
      )}

      <Card className="flex items-start gap-2 border-info/40 bg-info/5 p-3 text-xs">
        <AlertCircle className="mt-0.5 h-4 w-4 shrink-0 text-info" />
        <p className="text-muted-foreground">
          {t('adm.conn.only_m365')}
        </p>
      </Card>

      <M365ConnectorModal
        open={m365Open}
        onOpenChange={setM365Open}
        redirectURI={redirectURI}
        onSaved={async () => {
          await qc.invalidateQueries({ queryKey: ['admin', 'connectors'] })
        }}
      />
    </div>
  )
}

// Standalone URL redirects into the canonical Integrations shell tab.
// The page component stays exported — the shell embeds it — so this
// content renders in exactly one place.
export const Route = createFileRoute('/_authenticated/admin/connectors')({
  beforeLoad: () => {
    throw redirect({ to: '/admin/integrations', search: { tab: 'connectors' }, replace: true })
  },
})
