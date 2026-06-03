import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
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
  label: string
  description: string
  /** comingSoon = no backend provider exists yet under
   *  services/connector/internal/providers. The card shows a
   *  "Coming soon" badge instead of "Available", and the Install
   *  button is disabled so admins don't kick off an OAuth flow
   *  that would dead-end at the stub /auth-url handler. */
  comingSoon?: boolean
}

const CATALOG: ProviderDef[] = [
  { id: 'salesforce',   label: 'Salesforce',    description: 'Sync attachments + ContentDocument between Salesforce and a SeDoc workspace.' },
  { id: 'google_drive', label: 'Google Drive',  description: 'Two-way sync between a Drive folder and a SeDoc workspace.' },
  { id: 'm365',         label: 'Microsoft 365', description: 'Pull files from SharePoint / OneDrive sites into SeDoc.' },
  { id: 'dropbox',      label: 'Dropbox',       description: 'Mirror a Dropbox team folder into a SeDoc workspace.', comingSoon: true },
  { id: 'box',          label: 'Box',           description: 'Sync Box folders with SeDoc, mapping permissions per workspace.', comingSoon: true },
]

interface InstalledConnector {
  provider: string
  connected_at?: string
  status?: string
}

export function ConnectorsPage() {
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

  const install = useMutation({
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
      toast.message(`${out.provider} OAuth handshake not yet implemented`, {
        description: 'Backend returned a stub. The connector framework is wired; vendor-specific OAuth lands in a follow-up.',
      })
      qc.invalidateQueries({ queryKey: ['admin', 'connectors'] })
    },
    onError: (e: Error) => toast.error(e.message || 'Install failed'),
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
        title="Connectors"
        description="Sync documents to and from M365, Salesforce, Google Drive, Dropbox, and Box. Each connector is tenant-scoped and audited."
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
                      <Badge variant="active"><CheckCircle2 className="me-1 h-3 w-3" /> Installed</Badge>
                    ) : p.comingSoon ? (
                      <Badge variant="outline">Coming soon</Badge>
                    ) : (
                      <Badge variant="draft">Available</Badge>
                    )}
                  </header>
                  <p className="flex-1 text-xs text-muted-foreground">{p.description}</p>
                  <div className="mt-3">
                    <Button
                      size="sm"
                      variant={isInstalled ? 'outline' : 'default'}
                      onClick={() => handleInstallClick(p.id)}
                      loading={install.isPending && install.variables === p.id}
                      disabled={p.comingSoon}
                      title={p.comingSoon ? 'OAuth handshake not implemented yet — track ADR 0111 follow-ups for ETA.' : undefined}
                      data-testid={`install-${p.id}`}
                    >
                      <Sparkles className="me-1 h-3.5 w-3.5" />
                      {p.comingSoon ? 'Not available' : isInstalled ? 'Re-authorize' : 'Install'}
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
          Microsoft 365 has a real OAuth handshake (ADR 0111). The other tiles still hit a stub /auth-url
          endpoint until their providers get the same treatment.
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

export const Route = createFileRoute('/_authenticated/admin/connectors')({ component: ConnectorsPage })
